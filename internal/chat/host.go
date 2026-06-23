package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"plexus/pkg/brain"
	"plexus/pkg/llm"
	"plexus/pkg/mesh"
	"plexus/protocol"
)

// host.go is the Brain⇄bus bridge: it hosts an assembled chat agent on a mesh
// Node and turns the bus into the agent's driver. Frames arrive on the node's
// inbox (push, via OnMessage, one goroutine per message); a single worker drains
// them and drives the brain serially (history is not concurrency-safe). The
// worker handles user turns and the history-mutating control commands
// (reset/system); read-only control commands and approval answers are handled on
// the push goroutine. Streamed deltas, replies, approvals and control results
// all go back as frames on sys.report.

// channelInbound buffers pushed messages for the single worker to pull.
type channelInbound struct {
	ch chan protocol.Message
}

func newChannelInbound(buf int) *channelInbound {
	return &channelInbound{ch: make(chan protocol.Message, buf)}
}

func (c *channelInbound) recv(ctx context.Context) (protocol.Message, error) {
	select {
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	case m := <-c.ch:
		return m, nil
	}
}

func (c *channelInbound) push(m protocol.Message) { c.ch <- m }

// Host is a chat agent bound to the mesh.
type Host struct {
	agent         *Agent
	node          *mesh.Node
	inbound       *channelInbound
	approver      *busApprover
	gw            *mutableGateway // set when the gateway is runtime-reconfigurable
	agentID       string
	reportSubject string
	corr          atomic.Uint64
	curCorr       atomic.Value // string: correlation id of the turn the worker is on
	mu            sync.Mutex
	turnCancel    context.CancelFunc // cancels the in-flight turn (Ctrl-C resets the loop)
	thinkBuf      strings.Builder    // accumulates the turn's thinking (worker goroutine only)
}

// NewHost assembles a chat agent and binds it to a mesh node under agentID. cfg
// is completed with the bus approver and the streaming sinks. If cfg.Gateway is a
// *mutableGateway, control commands can reconfigure it at runtime.
func NewHost(ctx context.Context, agentID string, cfg Config, nodeOpts ...mesh.Option) (*Host, error) {
	in := newChannelInbound(64)
	h := &Host{inbound: in, agentID: agentID}
	h.curCorr.Store("")
	if mg, ok := cfg.Gateway.(*mutableGateway); ok {
		h.gw = mg
	}

	// Approval asks the user over the bus and blocks the loop until the answer is
	// demuxed back in onMessage.
	h.approver = newBusApprover(
		func(corr, desc string) { h.send(context.Background(), corr, Frame{Kind: kindApproval, Text: desc}) },
		func() string { return fmt.Sprintf("appr-%d", h.corr.Add(1)) },
	)

	// Stream deltas and per-turn usage back to the user, tagged with the turn the
	// worker is currently on (set before each Handle; same goroutine).
	cfg.Approver = h.approver
	cfg.OnDelta = func(d string) { h.send(context.Background(), h.turnCorr(), Frame{Kind: kindDelta, Text: d}) }
	cfg.OnUsage = func(u llm.Usage) {
		h.send(context.Background(), h.turnCorr(), Frame{Kind: kindUsage, Text: usageLine(u)})
	}
	// Thinking is a first-class interaction stream — always shown inline. It is
	// also buffered for a single obs.thinking emission per turn (so `plexus watch`
	// gets the full reasoning without per-delta flooding). Called on the worker
	// goroutine, so thinkBuf needs no lock.
	cfg.OnThinking = func(t string) {
		h.thinkBuf.WriteString(t)
		h.send(context.Background(), h.turnCorr(), Frame{Kind: kindThinking, Text: t})
	}
	// Tool/delegation activity is a first-class interaction signal (like thinking):
	// the user must see that the agent is working — especially during long-running
	// delegation, whose result lands only after the sub-task finishes. So the START
	// of every call is streamed inline as an always-on kindActivity frame.
	cfg.OnToolStart = func(name, args string) {
		sub := activityTool
		if name == brain.DelegateToolName {
			sub = activityDelegate
		}
		h.send(context.Background(), h.turnCorr(), Frame{Kind: kindActivity, Cmd: sub, Text: activityLine(name, args)})
	}
	// The COMPLETED call (with its result) is the verbose tap — observability over
	// the bus on the dedicated obs subject (sys.obs.<id>.trace), OFF the functional
	// report channel. Fire-and-forget: with no subscriber NATS drops it. Consumers
	// subscribe by wildcard (chat /trace, or `plexus watch`).
	cfg.OnTool = func(name, args, result string) {
		// A labelled multi-line block (name header, then args/result on their own
		// aligned lines) reads far better than one long `name(args) -> result`
		// line. The client indents the continuation lines under the [trace] tag;
		// `plexus watch` shows the raw block.
		body := fmt.Sprintf("%s\nargs:   %s\nresult: %s", name, preview(args, 400), preview(result, 1000))
		_ = h.node.Observe(context.Background(), "trace", []byte(body))
	}

	opts := append([]mesh.Option{mesh.WithOnMessage(h.onMessage)}, nodeOpts...)
	node := mesh.NewNode(agentID, opts...)
	h.node = node
	h.reportSubject = node.Options.ReportSubject

	agent, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	h.agent = agent
	return h, nil
}

func (h *Host) turnCorr() string {
	s, _ := h.curCorr.Load().(string)
	return s
}

func (h *Host) setTurnCancel(c context.CancelFunc) {
	h.mu.Lock()
	h.turnCancel = c
	h.mu.Unlock()
}

// cancelTurn aborts the in-flight turn (a /cancel — Ctrl-C). It resets only the
// current cognitive loop, never the agent or the workflow context.
func (h *Host) cancelTurn() {
	h.mu.Lock()
	c := h.turnCancel
	h.mu.Unlock()
	if c != nil {
		c()
	}
}

// onMessage demuxes a bus frame. Approval answers wake the approver; read-only
// control commands are handled here (concurrent-safe); user turns and the
// history-mutating reset/system controls are pushed to the worker.
func (h *Host) onMessage(m protocol.Message) {
	f, ok := decodeFrame(m.Payload)
	if !ok {
		h.inbound.push(m) // foreign/raw message — treat as a turn
		return
	}
	switch f.Kind {
	case kindAnswer:
		h.approver.resolve(m.CorrelationID, strings.EqualFold(strings.TrimSpace(f.Text), approveWord))
	case kindCancel:
		h.cancelTurn() // Ctrl-C: reset the in-flight turn only
	case kindCtrl:
		if isWorkerCtrl(f.Cmd) {
			h.inbound.push(m) // serialize with turns on the worker
			return
		}
		h.send(context.Background(), m.CorrelationID, Frame{Kind: kindCtrl, Text: h.runCtrl(context.Background(), f.Cmd, f.Arg)})
	default: // kindSay or anything else: a user turn
		h.inbound.push(m)
	}
}

// Run drains inbound and drives the brain serially until ctx is cancelled.
func (h *Host) Run(ctx context.Context) error {
	nodeErr := make(chan error, 1)
	go func() { nodeErr <- h.node.Run(ctx) }()

	for {
		msg, err := h.inbound.recv(ctx)
		if err != nil {
			break // ctx done
		}
		f, _ := decodeFrame(msg.Payload)
		h.curCorr.Store(msg.CorrelationID)

		if f.Kind == kindCtrl { // worker control: reset / system
			h.send(ctx, msg.CorrelationID, Frame{Kind: kindCtrl, Text: h.runWorkerCtrl(f.Cmd, f.Arg)})
			continue
		}

		text := string(msg.Payload)
		if f.Kind == kindSay {
			text = f.Text
		}
		// Per-turn context: Ctrl-C (a /cancel frame) cancels THIS turn only, never
		// the workflow ctx — the agent stays alive and returns to the prompt.
		tctx, tcancel := context.WithCancel(ctx)
		h.setTurnCancel(tcancel)
		h.thinkBuf.Reset()
		reply, err := h.agent.Brain.Handle(tctx, protocol.Message{
			Type:    protocol.TypeP2P,
			Sender:  "user",
			TaskID:  taskOr(msg.TaskID),
			Payload: []byte(text),
		})
		h.setTurnCancel(nil)
		if h.thinkBuf.Len() > 0 {
			_ = h.node.Observe(ctx, "thinking", []byte(h.thinkBuf.String())) // mirror to obs (one per turn)
		}
		// Capture whether the user interrupted THIS turn before we cancel tctx
		// ourselves (our own tcancel would otherwise make tctx.Err() non-nil).
		interrupted := tctx.Err() != nil
		tcancel()

		switch {
		case err == nil:
			h.send(ctx, msg.CorrelationID, Frame{Kind: kindReply, Text: reply, Done: true})
		case ctx.Err() != nil:
			return <-nodeErr // workflow shutting down
		case interrupted:
			// User interrupted this turn — reset the loop, stay alive.
			h.send(ctx, msg.CorrelationID, Frame{Kind: kindReply, Text: "[interrupted]", Done: true})
		default:
			slog.Error("chat: turn failed", "err", err)
			h.send(ctx, msg.CorrelationID, Frame{Kind: kindError, Text: err.Error()})
		}
	}
	return <-nodeErr
}

// send publishes a frame to the control plane's report subject, paired by corr.
func (h *Host) send(ctx context.Context, corr string, f Frame) {
	rep := protocol.Message{
		ID:            fmt.Sprintf("rep-%d", time.Now().UnixNano()),
		Sender:        h.agentID,
		Type:          protocol.TypeReport,
		CorrelationID: corr,
		Payload:       encodeFrame(f),
		Timestamp:     time.Now().Unix(),
	}
	if err := h.node.SendRaw(ctx, h.reportSubject, rep); err != nil {
		slog.Error("chat: failed to send frame", "err", err)
	}
}

// ObserveSubject returns the node's observability subject prefix, so the client
// can subscribe to this agent's obs streams (sys.obs.<id>.>).
func (h *Host) ObserveSubject() string { return h.node.Options.ObserveSubject }

// Close releases the agent's resources.
func (h *Host) Close() error { return h.agent.Close() }

func taskOr(t string) string {
	if t == "" {
		return DefaultTaskID
	}
	return t
}

func usageLine(u llm.Usage) string {
	return fmt.Sprintf("tokens: in=%d out=%d total=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

// activityLine formats the one-line, always-on activity marker shown when a
// tool/delegation call begins. Delegation renders distinctly (its objective)
// from an ordinary effector call (name + brief args), so the user can tell when
// a sub-task was spun up.
func activityLine(name, args string) string {
	if name == brain.DelegateToolName {
		if obj := delegateObjective(args); obj != "" {
			return "[delegate] " + preview(obj, 200)
		}
		return "[delegate] sub-task"
	}
	return "[tool] " + name + "(" + preview(args, 120) + ")"
}

// delegateObjective pulls the objective out of a delegate call's JSON arguments
// for the activity line; "" if it cannot be parsed.
func delegateObjective(args string) string {
	var a struct {
		Objective string `json:"objective"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	return a.Objective
}

// preview truncates s to n runes for a trace line, marking elision.
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
