package chat

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/chzyer/readline"
	"plexus/protocol"
	"plexus/server"
)

// client is the rich REPL on the control-plane side. It talks to the hosted
// agent only over the bus (frames in/out), never touching the brain directly.
// The flow is synchronous per turn: submit a message, stream the reply live,
// answer any approval inline, return to the prompt — mirroring the old direct
// REPL's feel while everything now rides the mesh.
type client struct {
	srv     *server.Server
	agentID string
	rl      *readline.Instance
	frames  chan inFrame
	turn    int
}

// inFrame is a decoded agent frame plus the message's correlation id (needed to
// pair an approval answer back to its request).
type inFrame struct {
	f    Frame
	corr string
}

// slashCommands drives /help and Tab completion.
var slashCommands = []string{
	"/key", "/provider", "/model", "/models", "/system", "/debug",
	"/status", "/tools", "/steps", "/memory", "/reset", "/trace", "/verbose",
	"/approve", "/deny", "/help", "/?", "/exit", "/quit", "/bye",
}

// hostCommands map a slash name to the control command the host runs.
var hostCommands = map[string]string{
	"/key": cmdKey, "/provider": cmdProvider, "/model": cmdModel, "/models": cmdModels,
	"/system": cmdSystem, "/debug": cmdDebug, "/status": cmdStatus, "/tools": cmdTools,
	"/steps": cmdSteps, "/memory": cmdMemory, "/reset": cmdReset,
	"/trace": cmdTrace, "/verbose": cmdTrace, // /verbose is an alias for /trace
}

// onReport decodes an agent frame and queues it for the active receive phase.
// Runs on the server's callback goroutine.
func (c *client) onReport(m protocol.Message) {
	if f, ok := decodeFrame(m.Payload); ok {
		c.frames <- inFrame{f: f, corr: m.CorrelationID}
	}
}

func (c *client) run(ctx context.Context) error {
	out := c.rl.Stdout()
	fmt.Fprintln(out, "Plexus chat — message the agent; /help for commands, /exit to quit")

	for {
		line, err := c.rl.Readline()
		if err == readline.ErrInterrupt {
			fmt.Fprintln(out, "(use /exit to quit)")
			continue
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			name, arg, _ := strings.Cut(input, " ")
			arg = strings.TrimSpace(arg)
			switch name {
			case "/exit", "/quit", "/bye":
				return nil
			case "/help", "/?":
				c.printHelp(out)
			case "/approve", "/deny":
				fmt.Fprintln(out, "(no pending approval)")
			default:
				if cmd, ok := hostCommands[name]; ok {
					c.control(ctx, cmd, arg)
				} else {
					fmt.Fprintf(out, "unknown command %q — /help\n", name)
				}
			}
			continue
		}

		c.say(ctx, input)
	}
}

// say sends a user turn and streams the reply (handling any approval inline).
func (c *client) say(ctx context.Context, text string) {
	c.turn++
	corr := fmt.Sprintf("turn-%d", c.turn)
	if !c.send(ctx, corr, Frame{Kind: kindSay, Text: text}, DefaultTaskID) {
		return
	}
	c.receive(ctx)
}

// control sends a control command and prints its single result.
func (c *client) control(ctx context.Context, cmd, arg string) {
	c.turn++
	corr := fmt.Sprintf("ctrl-%d", c.turn)
	if !c.send(ctx, corr, Frame{Kind: kindCtrl, Cmd: cmd, Arg: arg}, "") {
		return
	}
	out := c.rl.Stdout()
	for {
		select {
		case in := <-c.frames:
			if in.f.Kind == kindCtrl {
				if in.f.Text != "" {
					fmt.Fprintln(out, in.f.Text)
				}
				return
			}
			// ignore stray non-ctrl frames
		case <-ctx.Done():
			return
		}
	}
}

// receive consumes frames for the in-flight turn: print deltas live, answer
// approvals inline, finish on reply/error.
func (c *client) receive(ctx context.Context) {
	out := c.rl.Stdout()
	printed := false
	var usage string
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-c.frames:
			switch in.f.Kind {
			case kindDelta:
				fmt.Fprint(out, in.f.Text)
				printed = true
			case kindUsage:
				usage = in.f.Text
			case kindTrace:
				// dim trace line: · tool(args) → result
				fmt.Fprintf(out, "\033[2m· %s(%s) → %s\033[0m\n", in.f.Cmd, in.f.Arg, in.f.Text)
			case kindApproval:
				c.approval(ctx, in.corr, in.f.Text)
			case kindError:
				fmt.Fprintf(out, "\n[error: %s]\n", in.f.Text)
				return
			case kindReply:
				if !printed && in.f.Text != "" {
					fmt.Fprint(out, in.f.Text)
				}
				fmt.Fprintln(out)
				if usage != "" {
					fmt.Fprintf(out, "\033[2m[%s]\033[0m\n", usage)
				}
				return
			}
		}
	}
}

// approval prompts for /approve–/deny and sends the answer back, paired by corr.
func (c *client) approval(ctx context.Context, corr, desc string) {
	out := c.rl.Stdout()
	fmt.Fprintf(out, "\n\033[33m⚠ approval required:\033[0m %s\n", desc)
	for {
		line, err := c.rl.Readline()
		if err != nil {
			return
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "/approve", "approve", "y", "yes":
			c.send(ctx, corr, Frame{Kind: kindAnswer, Text: approveWord}, "")
			return
		case "/deny", "deny", "n", "no":
			c.send(ctx, corr, Frame{Kind: kindAnswer, Text: denyWord}, "")
			return
		default:
			fmt.Fprintln(out, "(type /approve or /deny)")
		}
	}
}

// send publishes a frame to the agent; returns false on failure.
func (c *client) send(ctx context.Context, corr string, f Frame, task string) bool {
	if err := c.srv.SendP2P(ctx, c.agentID, protocol.Message{
		CorrelationID: corr,
		TaskID:        task,
		Payload:       encodeFrame(f),
	}); err != nil {
		fmt.Fprintf(c.rl.Stdout(), "(send failed: %v)\n", err)
		return false
	}
	return true
}

func (c *client) printHelp(out io.Writer) {
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  /key <v>          set the LLM API key (start without one is fine)")
	fmt.Fprintln(out, "  /provider <p>     switch provider (openai|anthropic)")
	fmt.Fprintln(out, "  /model <id>       set the model id")
	fmt.Fprintln(out, "  /models           list the provider's models")
	fmt.Fprintln(out, "  /system <txt>     set the agent's system prompt (resets history)")
	fmt.Fprintln(out, "  /debug on|off     show raw LLM request body + response status")
	fmt.Fprintln(out, "  /status           show gateway config")
	fmt.Fprintln(out, "  /tools            list the agent's tools")
	fmt.Fprintln(out, "  /steps            show the agent's plan (checkpoint chain)")
	fmt.Fprintln(out, "  /memory           show the agent's working memory")
	fmt.Fprintln(out, "  /trace on|off     show each tool/delegation call (alias /verbose)")
	fmt.Fprintln(out, "  /reset            clear the conversation")
	fmt.Fprintln(out, "  /approve, /deny   answer a pending approval")
	fmt.Fprintln(out, "  /help, /exit      this help / quit (also Ctrl-D)")
}

func completer() *readline.PrefixCompleter {
	items := make([]readline.PrefixCompleterInterface, 0, len(slashCommands))
	for _, s := range slashCommands {
		items = append(items, readline.PcItem(s))
	}
	return readline.NewPrefixCompleter(items...)
}
