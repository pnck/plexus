package anthropic

import (
	"context"
	"encoding/json"

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
}

// NewProvider creates a new Anthropic provider.
func NewProvider(apiKey, model string) *Provider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Provider{
		client:    &client,
		model:     model,
		MaxTokens: 8192, // TODO: figure out proper value
	}
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
	if len(systemBlocks) > 0 {
		params.System = systemBlocks
	}
	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	return &anthropicStream{stream: stream}, nil
}

type anthropicStream struct {
	stream  *ssestream.Stream[anthropic.MessageStreamEventUnion]
	current llm.StreamEvent
}

func (s *anthropicStream) Next() bool {
	if !s.stream.Next() {
		return false
	}

	_ = s.stream.Current()
	event := llm.StreamEvent{}

	// Basic mapping. Need to properly switch on union variants.
	// event.DeltaText = ...

	s.current = event
	return true
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
