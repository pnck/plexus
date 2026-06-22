package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientName and clientVersion identify this client to MCP servers during the
// initialize handshake.
const (
	clientName    = "plexus"
	clientVersion = "0.1.0"
)

// Client is a connected MCP client. It wraps an underlying SDK session and
// exposes a small, stable API (ListTools / CallTool). A Client is obtained from
// one of the Connect* constructors and must be closed with Close.
//
// A Client is safe for concurrent use to the extent the underlying SDK session
// is; calls are otherwise stateless.
type Client struct {
	session *sdk.ClientSession
}

// Connector abstracts how a Client establishes its underlying transport. It
// exists so callers (and tests) can supply alternative transports (e.g. an
// in-memory pipe) without this package importing the SDK transport types at the
// call site.
type Connector interface {
	transport() sdk.Transport
}

// StdioServer describes an MCP server launched as a child process and spoken to
// over stdio (newline-delimited JSON on stdin/stdout). This is the primary,
// fully-supported transport.
type StdioServer struct {
	// Command is the executable to launch.
	Command string
	// Args are the arguments passed to Command.
	Args []string
	// Env, when non-nil, replaces the child process environment. When nil the
	// child inherits the parent environment.
	Env []string
}

func (s StdioServer) transport() sdk.Transport {
	cmd := exec.Command(s.Command, s.Args...) //nolint:gosec // command is operator-configured, not user input
	if s.Env != nil {
		cmd.Env = s.Env
	}
	return &sdk.CommandTransport{Command: cmd}
}

// rawTransport wraps an already-constructed SDK transport. It backs ConnectWith
// and is the extension point used by tests (in-memory transport) and future
// HTTP+SSE support.
type rawTransport struct{ t sdk.Transport }

func (r rawTransport) transport() sdk.Transport { return r.t }

// ConnectStdio launches the given server as a child process and connects over
// stdio. The returned Client owns the child process lifecycle; Close shuts it
// down.
func ConnectStdio(ctx context.Context, server StdioServer) (*Client, error) {
	return connect(ctx, server)
}

// ConnectWith connects using a caller-supplied Connector. This is the extension
// point for alternative transports (in-memory for tests today; HTTP+SSE later).
func ConnectWith(ctx context.Context, c Connector) (*Client, error) {
	return connect(ctx, c)
}

// ConnectHTTP is a placeholder for the HTTP+SSE / streamable-HTTP transport.
//
// TODO(E2.x): implement streamable-HTTP transport once a remote MCP server is
// in scope. The SDK exposes mcp.StreamableClientTransport; wiring it is a small
// addition behind this same Connector seam. Deferred per the E2.3 spec (HTTP+SSE
// may be stubbed).
func ConnectHTTP(_ context.Context, _ string) (*Client, error) {
	return nil, fmt.Errorf("mcp: HTTP+SSE transport not yet implemented (see ConnectHTTP TODO)")
}

func connect(ctx context.Context, c Connector) (*Client, error) {
	impl := &sdk.Implementation{Name: clientName, Version: clientVersion}
	cl := sdk.NewClient(impl, nil)
	session, err := cl.Connect(ctx, c.transport(), nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect: %w", err)
	}
	return &Client{session: session}, nil
}

// ListTools returns the tools currently advertised by the server. It follows
// pagination to completion so the returned slice is the full set.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	params := &sdk.ListToolsParams{}
	for {
		res, err := c.session.ListTools(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("mcp: list tools: %w", err)
		}
		for _, t := range res.Tools {
			info, err := toToolInfo(t)
			if err != nil {
				return nil, err
			}
			out = append(out, info)
		}
		if res.NextCursor == "" {
			break
		}
		params.Cursor = res.NextCursor
	}
	return out, nil
}

// CallTool invokes the named tool with the given JSON-object arguments. args may
// be nil for tools that take no input. A nil error with ToolResult.IsError ==
// true means the tool ran but reported a tool-level failure (feed it back to the
// LLM for self-correction); a non-nil error is a transport/protocol failure.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	params := &sdk.CallToolParams{Name: name}
	if len(args) > 0 {
		params.Arguments = json.RawMessage(args)
	}
	res, err := c.session.CallTool(ctx, params)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp: call tool %q: %w", name, err)
	}
	return toToolResult(res), nil
}

// Close terminates the session and, for stdio transports, the child process.
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	if err := c.session.Close(); err != nil {
		return fmt.Errorf("mcp: close: %w", err)
	}
	return nil
}

// toToolInfo projects an SDK tool descriptor into our SDK-agnostic ToolInfo. The
// server's input schema is normalized to raw JSON.
func toToolInfo(t *sdk.Tool) (ToolInfo, error) {
	var schema json.RawMessage
	if t.InputSchema != nil {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return ToolInfo{}, fmt.Errorf("mcp: marshal input schema for tool %q: %w", t.Name, err)
		}
		schema = raw
	}
	return ToolInfo{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
	}, nil
}

// toToolResult flattens an SDK CallToolResult into our ToolResult. Text content
// blocks are concatenated; non-text blocks render to a typed placeholder so the
// model still sees that something was returned.
func toToolResult(res *sdk.CallToolResult) ToolResult {
	var b strings.Builder
	for i, content := range res.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch c := content.(type) {
		case *sdk.TextContent:
			b.WriteString(c.Text)
		default:
			b.WriteString(fmt.Sprintf("[non-text content: %T]", content))
		}
	}
	return ToolResult{
		Content: b.String(),
		IsError: res.IsError,
	}
}
