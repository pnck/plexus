package anthropic

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"plexus/pkg/llm"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// Provider implements the llm.Provider interface for Anthropic models.
type Provider struct {
	client    *anthropic.Client
	model     string
	MaxTokens int64
	// thinkingBudget enables extended thinking with this token budget (≥1024);
	// 0 disables it. Set via WithReasoningEffort's level→budget mapping.
	thinkingBudget int64
}

// opts holds optional configuration for the provider constructor.
type opts struct {
	baseURL        string
	middleware     []llm.HTTPMiddleware
	thinkingBudget int64
}

// Option configures the provider via the functional-option pattern.
type Option func(*opts)

// WithBaseURL overrides the API base URL.
func WithBaseURL(url string) Option {
	return func(o *opts) {
		o.baseURL = url
	}
}

// WithMiddleware registers HTTP middleware (e.g. request/response logging).
func WithMiddleware(mw llm.HTTPMiddleware) Option {
	return func(o *opts) {
		o.middleware = append(o.middleware, mw)
	}
}

// WithReasoningEffort enables Anthropic extended thinking, mapping the neutral
// effort tier (one of llm.ReasoningEfforts) to a token budget. Empty/unknown
// disables it. The budget must stay below max_tokens, which GenerateStream
// enforces per request.
func WithReasoningEffort(level string) Option {
	return func(o *opts) {
		o.thinkingBudget = thinkingBudgetFor(level)
	}
}

// thinkingBudgetFor maps an effort tier to an Anthropic thinking budget (tokens,
// ≥1024). Anthropic's budget is continuous, so it can honor the agent's higher
// tiers (xhigh/max) with larger budgets instead of clamping. 0 = disabled.
func thinkingBudgetFor(level string) int64 {
	switch level {
	case llm.EffortMinimal:
		return 1024
	case llm.EffortLow:
		return 2048
	case llm.EffortMedium:
		return 4096
	case llm.EffortHigh:
		return 8192
	case llm.EffortXHigh:
		return 16384
	case llm.EffortMax:
		return 32768
	default:
		return 0
	}
}

// NewProvider creates a new Anthropic provider.
func NewProvider(apiKey, model string, options ...Option) *Provider {
	var o opts
	for _, fn := range options {
		fn(&o)
	}

	reqOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if o.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(o.baseURL))
	}
	for _, mw := range o.middleware {
		reqOpts = append(reqOpts, option.WithMiddleware(mw))
	}

	client := anthropic.NewClient(reqOpts...)
	return &Provider{
		client:         &client,
		model:          model,
		MaxTokens:      8192, // TODO: figure out proper value
		thinkingBudget: o.thinkingBudget,
	}
}

// ListModels enumerates the model IDs available to the configured account via
// the Anthropic /models endpoint, returning them sorted. It satisfies llm.ModelLister.
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	var ids []string
	pager := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

// GenerateStream calls the Anthropic Messages streaming API.
func (p *Provider) GenerateStream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDefinition) (llm.EventStream, error) {
	var anthropicMsgs []anthropic.MessageParam
	var systemBlocks []anthropic.TextBlockParam

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			// Anthropic separates system prompts from the main message array
			systemBlocks = append(systemBlocks, anthropic.TextBlockParam{Text: m.Content})
		case llm.RoleUser:
			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case llm.RoleAssistant:
			// Simplified mapping, real code needs to map tool calls correctly
			anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		case llm.RoleTool:
			// Map to tool result blocks
			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false)))
		}
	}

	var anthropicTools []anthropic.ToolUnionParam
	for _, t := range tools {
		var schema anthropic.ToolInputSchemaParam
		if b, err := json.Marshal(t.Parameters); err == nil {
			_ = json.Unmarshal(b, &schema)
		}
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			},
		})
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: p.MaxTokens,
		Messages:  anthropicMsgs,
	}
	if p.thinkingBudget > 0 {
		// Extended thinking: budget must be < max_tokens, so ensure room for the
		// answer on top of the thinking budget.
		if params.MaxTokens <= p.thinkingBudget {
			params.MaxTokens = p.thinkingBudget + p.MaxTokens
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(p.thinkingBudget)
	}
	if len(systemBlocks) > 0 {
		params.System = systemBlocks
	}
	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	return &anthropicStream{stream: stream}, nil
}

// pendingToolBlock accumulates a tool_use content block across input_json_delta events.
type pendingToolBlock struct {
	id    string
	name  string
	input strings.Builder
}

type anthropicStream struct {
	stream  *ssestream.Stream[anthropic.MessageStreamEventUnion]
	current llm.StreamEvent

	// pending queues assembled tool-call events so Next() can yield them one at a time.
	pending []llm.StreamEvent

	// blocks maps a content-block index to its in-progress tool_use accumulation.
	blocks map[int64]*pendingToolBlock

	// usage accumulates token counts: input from message_start, output from message_delta.
	usage llm.Usage
}

func (s *anthropicStream) Next() bool {
	// Drain queued assembled events first.
	if len(s.pending) > 0 {
		s.current = s.pending[0]
		s.pending = s.pending[1:]
		return true
	}

	for s.stream.Next() {
		switch ev := s.stream.Current().AsAny().(type) {
		case anthropic.MessageStartEvent:
			// Input usage is reported up front.
			s.usage.PromptTokens = ev.Message.Usage.InputTokens

		case anthropic.ContentBlockStartEvent:
			// A tool_use block opening — start accumulating its input JSON.
			block := ev.ContentBlock
			if block.Type == "tool_use" {
				if s.blocks == nil {
					s.blocks = map[int64]*pendingToolBlock{}
				}
				s.blocks[ev.Index] = &pendingToolBlock{id: block.ID, name: block.Name}
			}

		case anthropic.ContentBlockDeltaEvent:
			switch delta := ev.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if delta.Text != "" {
					s.current = llm.StreamEvent{DeltaText: delta.Text}
					return true
				}
			case anthropic.ThinkingDelta:
				if delta.Thinking != "" {
					s.current = llm.StreamEvent{DeltaThinking: delta.Thinking}
					return true
				}
			case anthropic.InputJSONDelta:
				if pb, ok := s.blocks[ev.Index]; ok {
					pb.input.WriteString(delta.PartialJSON)
				}
			}

		case anthropic.ContentBlockStopEvent:
			// A tool_use block closed — emit the assembled ToolCall.
			if pb, ok := s.blocks[ev.Index]; ok {
				delete(s.blocks, ev.Index)
				s.current = llm.StreamEvent{
					ToolCall: &llm.ToolCall{
						ID:        pb.id,
						Name:      pb.name,
						Arguments: pb.input.String(),
					},
				}
				return true
			}

		case anthropic.MessageDeltaEvent:
			// Carries stop_reason and the final (cumulative) output usage.
			s.usage.CompletionTokens = ev.Usage.OutputTokens
			s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
			usage := s.usage
			s.current = llm.StreamEvent{
				FinishReason: string(ev.Delta.StopReason),
				Usage:        &usage,
			}
			return true
		}
	}

	return false
}

func (s *anthropicStream) Current() llm.StreamEvent {
	return s.current
}

func (s *anthropicStream) Err() error {
	return s.stream.Err()
}

func (s *anthropicStream) Close() error {
	return s.stream.Close()
}
