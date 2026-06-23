package chat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"plexus/protocol"
	"plexus/server"
)

// client is the thin REPL on the control-plane side: it sends the user's input
// to the agent over the bus and prints the agent's reports. It never touches the
// brain directly — the user is a control-plane peer (E2.6.5). Approval requests
// arrive as marked reports and are answered with /approve – /deny.
type client struct {
	srv     *server.Server
	agentID string
	out     io.Writer

	mu      sync.Mutex
	pending string // CorrelationID of an outstanding approval, "" if none
	turn    atomic.Uint64
}

// onReport handles a report from the agent: an approval request enters
// /approve–/deny mode; anything else is printed as a reply. Runs on the server's
// callback goroutine.
func (c *client) onReport(m protocol.Message) {
	if desc, ok := parseApprovalRequest(string(m.Payload)); ok {
		c.mu.Lock()
		c.pending = m.CorrelationID
		c.mu.Unlock()
		fmt.Fprintf(c.out, "\n⚠ approval required: %s\n  type /approve or /deny\n> ", desc)
		return
	}
	fmt.Fprintf(c.out, "\n%s\n> ", string(m.Payload))
}

// loop reads user input until /exit or EOF, dispatching each line.
func (c *client) loop(ctx context.Context, in io.Reader) error {
	fmt.Fprintln(c.out, "plexus chat — message the agent, /approve · /deny for approvals, /exit to quit")
	sc := bufio.NewScanner(in)
	for {
		fmt.Fprint(c.out, "> ")
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		switch line {
		case "":
			continue
		case "/exit", "/quit", "/bye":
			return nil
		case "/approve", "/deny":
			c.answer(ctx, line == "/approve")
		default:
			c.send(ctx, line)
		}
	}
}

// answer responds to an outstanding approval request.
func (c *client) answer(ctx context.Context, approved bool) {
	c.mu.Lock()
	corr := c.pending
	c.pending = ""
	c.mu.Unlock()
	if corr == "" {
		fmt.Fprintln(c.out, "(no pending approval)")
		return
	}
	word := denyWord
	if approved {
		word = approveWord
	}
	if err := c.srv.SendP2P(ctx, c.agentID, protocol.Message{CorrelationID: corr, Payload: []byte(word)}); err != nil {
		fmt.Fprintf(c.out, "(failed to send answer: %v)\n", err)
	}
}

// send delivers a user turn to the agent over the bus, scoped to the standing
// chat task and tagged with a fresh per-turn correlation id.
func (c *client) send(ctx context.Context, text string) {
	corr := fmt.Sprintf("turn-%d", c.turn.Add(1))
	if err := c.srv.SendP2P(ctx, c.agentID, protocol.Message{
		CorrelationID: corr,
		TaskID:        DefaultTaskID,
		Payload:       []byte(text),
	}); err != nil {
		fmt.Fprintf(c.out, "(failed to send: %v)\n", err)
	}
}
