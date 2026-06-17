package openai

import (
	"context"

	"plexus/agent/llm"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/shared"
)

// Provider implements the llm.Provider interface for OpenAI models using the official SDK.
type Provider struct {
	client *openai.Client
	model  string
}

// NewProvider creates a new OpenAI provider.
func NewProvider(apiKey, model string) *Provider {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &Provider{
		client: &client,
		model:  model,
	}
}

// GenerateStream calls the OpenAI Chat Completions streaming API.
func (p *Provider) GenerateStream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDefinition) (llm.EventStream, error) {
	// Map our unified messages to OpenAI specific formats
	var oaiMsgs []openai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			oaiMsgs = append(oaiMsgs, openai.SystemMessage(m.Content))
		case llm.RoleUser:
			oaiMsgs = append(oaiMsgs, openai.UserMessage(m.Content))
		case llm.RoleAssistant:
			// Handle assistant messages and potential tool calls
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
			oaiMsgs = append(oaiMsgs, openai.ToolMessage(m.ToolCallID, m.Content))
		}
	}

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
	}
	if len(oaiTools) > 0 {
		params.Tools = oaiTools
	}

	// Initiate the stream
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	return &openaiStream{stream: stream}, nil
}

type openaiStream struct {
	stream  *ssestream.Stream[openai.ChatCompletionChunk]
	current llm.StreamEvent
}

func (s *openaiStream) Next() bool {
	if !s.stream.Next() {
		return false
	}

	chunk := s.stream.Current()
	event := llm.StreamEvent{}

	if len(chunk.Choices) > 0 {
		choice := chunk.Choices[0]

		// Check for text delta
		if choice.Delta.Content != "" {
			event.DeltaText = choice.Delta.Content
		}

		// Check for tool calls delta
		if len(choice.Delta.ToolCalls) > 0 {
			tc := choice.Delta.ToolCalls[0]
			event.ToolCall = &llm.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}
		}

		// Check finish reason
		if choice.FinishReason != "" {
			event.FinishReason = string(choice.FinishReason)
		}
	}

	s.current = event
	return true
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
