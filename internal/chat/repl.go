package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/chzyer/readline"
	"github.com/nats-io/nats.go"
	"plexus/protocol"
	"plexus/server"
)

// ANSI colors for the side-channel streams, so the eye can separate them from
// the answer (which stays the terminal's default color) and from each other.
// Each kind owns one hue; reset closes a run.
const (
	colReset    = "\033[0m"
	colThink    = "\033[90m"   // thinking — gray, subdued (it is verbose by nature)
	colTool     = "\033[36m"   // tool call — cyan
	colDelegate = "\033[35m"   // delegation — magenta (spawns sub-cognition; stands out)
	colTrace    = "\033[2;37m" // verbose /trace obs — dim
	colApproval = "\033[33m"   // approval prompt — yellow
	colError    = "\033[31m"   // turn error — red
	colUsage    = "\033[2m"    // token usage — dim
)

// client is the rich REPL on the control-plane side. It talks to the hosted
// agent only over the bus (frames in/out), never touching the brain directly.
// The flow is synchronous per turn: submit a message, stream the reply live,
// answer any approval inline, return to the prompt — mirroring the old direct
// REPL's feel while everything now rides the mesh.
type client struct {
	srv       *server.Server
	nc        *nats.Conn // for subscribing to the agent's observability streams
	agentID   string
	obsPrefix string // the agent's ObserveSubject prefix (e.g. "sys.obs.")
	rl        *readline.Instance
	frames    chan inFrame
	turn      int
	obsSub    *nats.Subscription // non-nil while /trace is on
}

// inFrame is a decoded agent frame plus the message's correlation id (needed to
// pair an approval answer back to its request).
type inFrame struct {
	f    Frame
	corr string
}

// slashCommands drives /help and Tab completion.
var slashCommands = []string{
	"/key", "/provider", "/model", "/models", "/system", "/debug", "/reasoning",
	"/status", "/tools", "/steps", "/memory", "/reset", "/trace", "/verbose",
	"/approve", "/deny", "/help", "/?", "/exit", "/quit", "/bye",
}

// hostCommands map a slash name to the control command the host runs.
var hostCommands = map[string]string{
	"/key": cmdKey, "/provider": cmdProvider, "/model": cmdModel, "/models": cmdModels,
	"/system": cmdSystem, "/debug": cmdDebug, "/status": cmdStatus, "/tools": cmdTools,
	"/steps": cmdSteps, "/memory": cmdMemory, "/reset": cmdReset, "/reasoning": cmdReasoning,
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
			case "/trace", "/verbose":
				c.trace(out, arg)
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
	c.receive(ctx, corr)
}

// trace toggles a subscription to the agent's observability streams
// (sys.obs.<id>.>). The streams are always emitted by the agent; we only
// subscribe while /trace is on, so there is no client-side cost when off.
func (c *client) trace(out io.Writer, arg string) {
	on := c.obsSub != nil
	switch arg {
	case "": // no-arg = get
		if on {
			fmt.Fprintln(out, "trace = on")
		} else {
			fmt.Fprintln(out, "trace = off")
		}
		return
	case "on":
		if on {
			fmt.Fprintln(out, "(trace already on)")
			return
		}
		sub, err := c.nc.Subscribe(c.obsPrefix+c.agentID+".>", c.onObs)
		if err != nil {
			fmt.Fprintf(out, "(trace subscribe failed: %v)\n", err)
			return
		}
		c.obsSub = sub
		fmt.Fprintln(out, "trace on — tool/delegation calls will be shown")
	case "off":
		if on {
			_ = c.obsSub.Unsubscribe()
			c.obsSub = nil
		}
		fmt.Fprintln(out, "trace off")
	default:
		fmt.Fprintln(out, "usage: /trace on|off")
	}
}

// onObs receives an observability event and funnels it into the frame stream so
// the single display goroutine renders it in order (runs on a NATS goroutine).
func (c *client) onObs(m *nats.Msg) {
	var msg protocol.Message
	if err := json.Unmarshal(m.Data, &msg); err != nil {
		return
	}
	kind := m.Subject[strings.LastIndexByte(m.Subject, '.')+1:] // trailing token (trace/raw/…)
	select {
	case c.frames <- inFrame{f: Frame{Kind: kindTrace, Cmd: kind, Text: string(msg.Payload)}}:
	default: // drop under backpressure — a debug stream, never block the dispatcher
	}
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
// approvals inline, finish on reply/error. While waiting it catches Ctrl-C
// (SIGINT) and asks the agent to reset just this turn — the agent and session
// stay alive (the workflow context is never cancelled by Ctrl-C).
func (c *client) receive(ctx context.Context, corr string) {
	out := c.rl.Stdout()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	printed := false
	thinking := false // currently rendering a (dim) thinking run
	midLine := false  // un-terminated answer text is on the current line
	var usage string
	// endThinking closes the colored thinking run before answer/terminal output.
	endThinking := func() {
		if thinking {
			fmt.Fprint(out, colReset+"\n")
			thinking = false
		}
	}
	// freshLine ensures the next output starts at column 0 (so a dim activity
	// line never glues onto streamed answer text).
	freshLine := func() {
		endThinking()
		if midLine {
			fmt.Fprintln(out)
			midLine = false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			fmt.Fprint(out, "\n\033[2m^C — resetting this turn…\033[0m\n")
			c.send(ctx, corr, Frame{Kind: kindCancel}, "")
			// keep reading until the agent's terminal frame ([interrupted]) arrives
		case in := <-c.frames:
			switch in.f.Kind {
			case kindThinking:
				if !thinking {
					fmt.Fprint(out, colThink+"[think] ")
					thinking = true
				}
				fmt.Fprint(out, in.f.Text)
			case kindDelta:
				endThinking()
				fmt.Fprint(out, in.f.Text)
				printed = true
				midLine = !strings.HasSuffix(in.f.Text, "\n")
			case kindActivity:
				// always-on activity marker (tool/delegation starting). Cmd carries
				// the subtype so the color is chosen by semantics, not text matching.
				freshLine()
				col := colTool
				if in.f.Cmd == activityDelegate {
					col = colDelegate
				}
				fmt.Fprintf(out, "%s%s%s\n", col, in.f.Text, colReset)
			case kindUsage:
				usage = in.f.Text
			case kindTrace:
				// verbose observability block from the obs stream (Text is the
				// preformatted multi-line trace body).
				freshLine()
				printTrace(out, in.f.Text)
			case kindApproval:
				c.approval(ctx, in.corr, in.f.Text)
			case kindError:
				endThinking()
				fmt.Fprintf(out, "%s\n[error: %s]%s\n", colError, in.f.Text, colReset)
				return
			case kindReply:
				endThinking()
				if !printed && in.f.Text != "" {
					fmt.Fprint(out, in.f.Text)
				}
				fmt.Fprintln(out)
				if usage != "" {
					fmt.Fprintf(out, "%s[%s]%s\n", colUsage, usage, colReset)
				}
				return
			}
		}
	}
}

// approval prompts for /approve–/deny and sends the answer back, paired by corr.
func (c *client) approval(ctx context.Context, corr, desc string) {
	out := c.rl.Stdout()
	fmt.Fprintf(out, "\n%s[approval required]%s %s\n", colApproval, colReset, desc)
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

// printTrace renders a multi-line trace body dim, tagging the first line with
// [trace] and indenting the continuation lines (args:/result:) to align under
// it. Each line is colored independently so the dim run never bleeds past a
// newline into the prompt.
func printTrace(out io.Writer, body string) {
	const indent = "        " // len("[trace] ")
	lines := strings.Split(body, "\n")
	fmt.Fprintf(out, "%s[trace] %s%s\n", colTrace, lines[0], colReset)
	for _, ln := range lines[1:] {
		fmt.Fprintf(out, "%s%s%s%s\n", colTrace, indent, ln, colReset)
	}
}

func (c *client) printHelp(out io.Writer) {
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  /key <v>          set the LLM API key (start without one is fine)")
	fmt.Fprintln(out, "  /provider <p>     switch provider (openai|anthropic)")
	fmt.Fprintln(out, "  /model <id>       set the model id")
	fmt.Fprintln(out, "  /models           list the provider's models")
	fmt.Fprintln(out, "  /system <txt>     set the agent's system prompt (resets history)")
	fmt.Fprintln(out, "  /debug on|off     show raw LLM request body + response status")
	fmt.Fprintln(out, "  /reasoning <lvl>  reasoning effort: minimal|low|medium|high|xhigh|max|off")
	fmt.Fprintln(out, "  /status           show gateway config")
	fmt.Fprintln(out, "  /tools            list the agent's tools")
	fmt.Fprintln(out, "  /steps            show the agent's plan (checkpoint chain)")
	fmt.Fprintln(out, "  /memory           show the agent's working memory")
	fmt.Fprintln(out, "  /trace on|off     verbose tool/delegation results + raw obs (alias /verbose)")
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
