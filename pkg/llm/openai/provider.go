package openai

import (
	"context"
	"hash/fnv"
	"io"
	"sort"
	"strconv"
	"strings"

	"plexus/pkg/llm"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/shared"
)

// Provider implements the llm.Provider interface for OpenAI models using the official SDK.
type Provider struct {
	client          *openai.Client
	model           string
	reasoningEffort string // "" | low | medium | high (o-series only)
	// suppressThinking sends enable_thinking:false on each request — a best-effort
	// way to turn OFF thinking on compatible gateways that think by default
	// (Qwen/DashScope). Endpoints that don't know the field ignore it.
	suppressThinking bool
}

// opts holds optional configuration for the provider constructor.
type opts struct {
	baseURL          string
	middleware       []llm.HTTPMiddleware
	reasoningEffort  string
	suppressThinking bool
}

// Option configures the provider via the functional-option pattern.
type Option func(*opts)

// WithBaseURL overrides the API base URL (e.g. for OpenAI-compatible gateways).
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

// WithReasoningEffort sets the reasoning effort (one of llm.ReasoningEfforts);
// empty disables it. Mapped to OpenAI's range by openaiEffort.
func WithReasoningEffort(level string) Option {
	return func(o *opts) {
		o.reasoningEffort = level
	}
}

// WithSuppressThinking makes each request carry enable_thinking:false, a
// best-effort opt-out for compatible gateways (Qwen/DashScope) that emit
// thinking by default. Standard OpenAI does not think-by-default, so callers
// should only set this for non-default endpoints (the chat gateway gates it on a
// custom base URL). Unknown-field-tolerant endpoints simply ignore it.
func WithSuppressThinking() Option {
	return func(o *opts) {
		o.suppressThinking = true
	}
}

// openaiEffort maps a neutral effort tier to OpenAI's reasoning_effort. OpenAI's
// range is minimal..high, so the agent's higher tiers (xhigh, max) clamp to
// high. Empty/unknown → "" (not sent).
func openaiEffort(level string) string {
	switch level {
	case llm.EffortMinimal, llm.EffortLow, llm.EffortMedium, llm.EffortHigh:
		return level
	case llm.EffortXHigh, llm.EffortMax:
		return llm.EffortHigh // OpenAI tops out at high
	default:
		return ""
	}
}

// NewProvider creates a new OpenAI provider.
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

	client := openai.NewClient(reqOpts...)
	return &Provider{
		client:           &client,
		model:            model,
		reasoningEffort:  o.reasoningEffort,
		suppressThinking: o.suppressThinking,
	}
}

// ListModels enumerates the model IDs available to the configured account via
// the OpenAI /models endpoint, returning them sorted. It satisfies llm.ModelLister.
func (p *Provider) ListModels(ctx context.Context) ([]string, error) {
	var ids []string
	pager := p.client.Models.ListAutoPaging(ctx)
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

// GenerateStream calls the OpenAI Chat Completions streaming API.
func (p *Provider) GenerateStream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDefinition) (llm.EventStream, error) {
	oaiMsgs := toOpenAIMessages(msgs)

	// Map tools
	var oaiTools []openai.ChatCompletionToolParam
	for _, t := range tools {
		paramsMap, _ := t.Parameters.(map[string]any)
		oaiTools = append(oaiTools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  shared.FunctionParameters(paramsMap),
			},
		})
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(p.model),
		Messages: oaiMsgs,
		// Request a final usage chunk after generation completes.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if len(oaiTools) > 0 {
		params.Tools = oaiTools
	}
	if e := openaiEffort(p.reasoningEffort); e != "" {
		params.ReasoningEffort = shared.ReasoningEffort(e)
	}
	// OpenAI caches prompt prefixes AUTOMATICALLY (no explicit breakpoints like
	// Anthropic). A stable prompt_cache_key routes same-prefix requests to the same
	// cache for a better hit rate; we scope it to the system prefix (the first
	// shared breakpoint), which is constant across a session — see llm.CacheBreakpoints.
	if key := systemPrefixCacheKey(msgs); key != "" {
		params.PromptCacheKey = openai.String(key)
	}

	// Best-effort thinking opt-out for gateways that think by default. Sent as an
	// extra body field (not a typed param) since it is a vendor extension.
	var reqOpts []option.RequestOption
	if p.suppressThinking {
		reqOpts = append(reqOpts, option.WithJSONSet("enable_thinking", false))
	}

	// Initiate the stream
	stream := p.client.Chat.Completions.NewStreaming(ctx, params, reqOpts...)

	return &openaiStream{stream: stream}, nil
}

// toOpenAIMessages maps our unified messages to OpenAI's request format,
// including assistant tool-call turns and tool results. Extracted so the
// mapping — in particular the tool_call_id pairing — is unit-testable without a
// live endpoint.
// systemPrefixCacheKey derives a stable prompt_cache_key from the conversation's
// system prefix (kernel + role card), the first breakpoint llm.CacheBreakpoints
// returns. OpenAI auto-caches prefixes, so this only routes same-prefix requests
// to the same cache; it is constant across a session and changes when the role
// card does (/system). Empty when there is no system prefix.
func systemPrefixCacheKey(msgs []llm.Message) string {
	bps := llm.CacheBreakpoints(msgs)
	if len(bps) == 0 {
		return ""
	}
	h := fnv.New64a()
	for _, m := range msgs[:bps[0]+1] {
		_, _ = io.WriteString(h, string(m.Role))
		_, _ = io.WriteString(h, m.Content)
		_, _ = h.Write([]byte{0})
	}
	return "plexus-" + strconv.FormatUint(h.Sum64(), 16)
}

func toOpenAIMessages(msgs []llm.Message) []openai.ChatCompletionMessageParamUnion {
	var oaiMsgs []openai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			oaiMsgs = append(oaiMsgs, openai.SystemMessage(m.Content))
		case llm.RoleUser:
			oaiMsgs = append(oaiMsgs, openai.UserMessage(m.Content))
		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var calls []openai.ChatCompletionMessageToolCallParam
				for _, tc := range m.ToolCalls {
					calls = append(calls, openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					})
				}
				oaiMsgs = append(oaiMsgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(m.Content)},
						ToolCalls: calls,
					},
				})
			} else {
				oaiMsgs = append(oaiMsgs, openai.AssistantMessage(m.Content))
			}
		case llm.RoleTool:
			// SDK signature is ToolMessage(content, toolCallID) — content first.
			oaiMsgs = append(oaiMsgs, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return oaiMsgs
}

// pendingToolCall accumulates fragmented tool-call deltas across chunks, keyed by Index.
type pendingToolCall struct {
	id   string
	name string
	args strings.Builder
}

type openaiStream struct {
	stream  *ssestream.Stream[openai.ChatCompletionChunk]
	current llm.StreamEvent

	// pending holds tool-call and terminal events assembled at generation end,
	// to be yielded one-by-one by Next().
	pending []llm.StreamEvent

	// toolCalls accumulates tool-call fragments by Index; order preserves arrival.
	toolCalls map[int64]*pendingToolCall
	toolOrder []int64

	finishReason string
	usage        *llm.Usage
	flushed      bool

	// think splits inline <think>…</think> in the content stream.
	think thinkSplitter
}

func (s *openaiStream) Next() bool {
	// Drain any assembled events queued from a finished generation first.
	if len(s.pending) > 0 {
		s.current = s.pending[0]
		s.pending = s.pending[1:]
		return true
	}

	for s.stream.Next() {
		chunk := s.stream.Current()

		// Usage arrives on its own terminal chunk (choices empty).
		if chunk.Usage.TotalTokens != 0 || chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
			s.usage = &llm.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			// Accumulate tool-call fragments by Index across chunks.
			for _, tc := range choice.Delta.ToolCalls {
				pending, ok := s.toolCalls[tc.Index]
				if !ok {
					pending = &pendingToolCall{}
					if s.toolCalls == nil {
						s.toolCalls = map[int64]*pendingToolCall{}
					}
					s.toolCalls[tc.Index] = pending
					s.toolOrder = append(s.toolOrder, tc.Index)
				}
				if tc.ID != "" {
					pending.id = tc.ID
				}
				if tc.Function.Name != "" {
					pending.name = tc.Function.Name
				}
				pending.args.WriteString(tc.Function.Arguments)
			}

			if choice.FinishReason != "" {
				s.finishReason = string(choice.FinishReason)
			}

			// Assemble this chunk's user-visible events: reasoning_content (a
			// non-standard thinking field) first, then content split on <think>.
			var evs []llm.StreamEvent
			if rc := reasoningExtra(choice.Delta.JSON.ExtraFields); rc != "" {
				evs = append(evs, llm.StreamEvent{DeltaThinking: rc})
			}
			if choice.Delta.Content != "" {
				evs = append(evs, s.think.feed(choice.Delta.Content)...)
			}
			if len(evs) > 0 {
				s.current = evs[0]
				s.pending = append(s.pending, evs[1:]...)
				return true
			}
		}

		// A non-empty text delta is the only thing yielded mid-stream above;
		// otherwise keep reading until the stream ends.
	}

	// Stream ended: assemble tool calls and the terminal event exactly once.
	if !s.flushed {
		s.flushed = true
		s.pending = append(s.pending, s.think.flush()...) // any carried partial tag
		s.assembleFinalEvents()
		if len(s.pending) > 0 {
			s.current = s.pending[0]
			s.pending = s.pending[1:]
			return true
		}
	}

	return false
}

// assembleFinalEvents builds the queue of tool-call events plus the terminal event.
func (s *openaiStream) assembleFinalEvents() {
	for _, idx := range s.toolOrder {
		pc := s.toolCalls[idx]
		s.pending = append(s.pending, llm.StreamEvent{
			ToolCall: &llm.ToolCall{
				ID:        pc.id,
				Name:      pc.name,
				Arguments: pc.args.String(),
			},
		})
	}

	// Terminal event carries finish reason and usage.
	if s.finishReason != "" || s.usage != nil {
		s.pending = append(s.pending, llm.StreamEvent{
			FinishReason: s.finishReason,
			Usage:        s.usage,
		})
	}
}

func (s *openaiStream) Current() llm.StreamEvent {
	return s.current
}

func (s *openaiStream) Err() error {
	return s.stream.Err()
}

func (s *openaiStream) Close() error {
	return s.stream.Close()
}
