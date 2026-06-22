package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// echoIn is the input for the in-process test tool.
type echoIn struct {
	Text string `json:"text"`
}

// startMockServer spins up an in-process MCP server exposing two tools over an
// in-memory transport, and returns a Client connected to it. No network or child
// process is involved.
func startMockServer(t *testing.T) *Client {
	t.Helper()

	server := sdk.NewServer(&sdk.Implementation{Name: "mock", Version: "0.0.1"}, nil)

	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "echo the input text"},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
		})

	sdk.AddTool(server, &sdk.Tool{Name: "boom", Description: "always fails at the tool level"},
		func(_ context.Context, _ *sdk.CallToolRequest, _ echoIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "kaboom"}},
				IsError: true,
			}, nil, nil
		})

	clientT, serverT := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	c, err := ConnectWith(ctx, rawTransport{t: clientT})
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestListTools(t *testing.T) {
	c := startMockServer(t)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]ToolInfo{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	echo, ok := byName["echo"]
	if !ok {
		t.Fatalf("echo tool missing; got %v", tools)
	}
	if echo.Description != "echo the input text" {
		t.Fatalf("echo description=%q", echo.Description)
	}
	// Input schema should be present and valid JSON (an object schema).
	if len(echo.InputSchema) == 0 {
		t.Fatal("echo input schema empty")
	}
	var schema map[string]any
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil {
		t.Fatalf("input schema not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("input schema type=%v want object", schema["type"])
	}
}

func TestCallTool(t *testing.T) {
	c := startMockServer(t)
	args, _ := json.Marshal(echoIn{Text: "hello plexus"})
	res, err := c.CallTool(context.Background(), "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	if res.Content != "hello plexus" {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestCallToolToolLevelError(t *testing.T) {
	c := startMockServer(t)
	args, _ := json.Marshal(echoIn{Text: "x"})
	res, err := c.CallTool(context.Background(), "boom", args)
	if err != nil {
		t.Fatalf("transport error should be nil for tool-level failure: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for boom tool")
	}
	if res.Content != "kaboom" {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestConnectHTTPNotImplemented(t *testing.T) {
	if _, err := ConnectHTTP(context.Background(), "http://example"); err == nil {
		t.Fatal("expected not-implemented error from ConnectHTTP")
	}
}
