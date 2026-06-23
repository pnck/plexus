package chat

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"plexus/pkg/mesh"
	"plexus/protocol"
)

// host.go is the Brain⇄bus bridge (E2.6.2/.4): it hosts an assembled chat agent
// on a mesh Node and turns the bus into the brain's structured Inbound. Inbound
// messages arrive on the node's inbox (push, via OnMessage); a single worker
// drives the brain serially (the brain's history is not concurrency-safe) and
// reports each reply back over the bus. Approval requests and their answers ride
// the same bus, demuxed here (see onMessage). There is no session — every turn
// is scoped by the message's TaskID (the standing chat task, §5.7.10).

// channelInbound adapts the node's push-style OnMessage callback to the brain's
// pull-style Inbound seam, serializing access: pushes are buffered, and the
// single worker pulls one at a time via Recv. It records the most recently
// received message so the worker can pair the reply's CorrelationID.
type channelInbound struct {
	ch   chan protocol.Message
	last protocol.Message
}

func newChannelInbound(buf int) *channelInbound {
	return &channelInbound{ch: make(chan protocol.Message, buf)}
}

// Recv returns the next inbound message, or ctx error on shutdown.
func (c *channelInbound) Recv(ctx context.Context) (protocol.Message, error) {
	select {
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	case m := <-c.ch:
		c.last = m // safe: only the single worker reads `last`, after Recv returns
		return m, nil
	}
}

// push enqueues an inbound message (called from the node's OnMessage goroutine).
func (c *channelInbound) push(m protocol.Message) { c.ch <- m }

// Host is a chat agent bound to the mesh: an assembled Agent whose brain is fed
// by the bus, whose replies report back to the control plane, and whose approval
// requests round-trip to the user over the bus.
type Host struct {
	agent         *Agent
	node          *mesh.Node
	inbound       *channelInbound
	approver      *busApprover
	agentID       string
	reportSubject string
	corr          atomic.Uint64
}

// NewHost assembles a chat agent and binds it to a mesh node under agentID. cfg
// is completed with a channel-backed Inbound and the bus approver (any
// cfg.Inbound / cfg.Approver are overridden). nodeOpts carry transport config
// (e.g. mesh.WithNATSConn / WithNatsURL).
func NewHost(ctx context.Context, agentID string, cfg Config, nodeOpts ...mesh.Option) (*Host, error) {
	in := newChannelInbound(64)
	h := &Host{inbound: in, agentID: agentID}

	// The approver asks the user over the bus (an approval-request report) and
	// blocks the cognitive loop until the answer is demuxed back in onMessage.
	h.approver = newBusApprover(
		func(corr, desc string) {
			h.sendReport(context.Background(), corr, DefaultTaskID, markApprovalRequest(desc))
		},
		func() string { return fmt.Sprintf("appr-%d", h.corr.Add(1)) },
	)

	opts := append([]mesh.Option{mesh.WithOnMessage(h.onMessage)}, nodeOpts...)
	node := mesh.NewNode(agentID, opts...)
	h.node = node
	h.reportSubject = node.Options.ReportSubject

	cfg.Inbound = in
	cfg.Approver = h.approver
	agent, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	h.agent = agent
	return h, nil
}

// onMessage demuxes an inbound bus message: an approval answer (its
// CorrelationID matches a pending approval) wakes the blocked approver and is NOT
// forwarded to the brain; anything else is a user turn pushed to the brain's
// Inbound.
func (h *Host) onMessage(m protocol.Message) {
	if m.CorrelationID != "" && h.approver.resolve(m.CorrelationID, isApproveAnswer(m.Payload)) {
		return // it was an approval answer
	}
	h.inbound.push(m)
}

// Run connects the node (inbox subscription + registration) and processes
// inbound messages serially until ctx is cancelled. Each converged reply — and
// each turn-level error — reports back over the bus, paired by CorrelationID.
func (h *Host) Run(ctx context.Context) error {
	nodeErr := make(chan error, 1)
	go func() { nodeErr <- h.node.Run(ctx) }()

	for {
		reply, err := h.agent.Brain.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break // shutting down
			}
			slog.Error("chat: turn failed", "err", err)
			last := h.inbound.last
			h.sendReport(ctx, last.CorrelationID, last.TaskID, "error: "+err.Error())
			continue
		}
		last := h.inbound.last
		h.sendReport(ctx, last.CorrelationID, last.TaskID, reply)
	}
	return <-nodeErr
}

// sendReport publishes a message to the control plane's report subject, tagged
// with the given correlation id and task id.
func (h *Host) sendReport(ctx context.Context, corr, taskID, text string) {
	rep := protocol.Message{
		ID:            fmt.Sprintf("rep-%d", time.Now().UnixNano()),
		Sender:        h.agentID,
		Type:          protocol.TypeReport,
		CorrelationID: corr,
		TaskID:        taskID,
		Payload:       []byte(text),
		Timestamp:     time.Now().Unix(),
	}
	if err := h.node.SendRaw(ctx, h.reportSubject, rep); err != nil {
		slog.Error("chat: failed to report", "err", err)
	}
}

// Close releases the agent's resources.
func (h *Host) Close() error { return h.agent.Close() }
