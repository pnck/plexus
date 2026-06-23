// Package mesh provides the node SDK for the Plexus mesh.
// It allows individual services to connect to the mesh, register themselves,
// join broadcast or queue groups, and exchange messages.
package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"plexus/protocol"
)

// Node represents a Plexus mesh node
type Node struct {
	ID      string
	Options Options
	nc      *nats.Conn

	mu           sync.Mutex
	groups       map[string]*nats.Subscription
	pendingGroup map[string]string // key: topic/group, value: queueName (empty if broadcast)
}

// NewNode creates a new Node instance
func NewNode(id string, opts ...Option) *Node {
	options := DefaultOptions()
	for _, o := range opts {
		o(&options)
	}

	return &Node{
		ID:           id,
		Options:      options,
		groups:       make(map[string]*nats.Subscription),
		pendingGroup: make(map[string]string),
	}
}

// Run connects to NATS and starts listening for commands
func (a *Node) Run(ctx context.Context) error {
	if a.Options.NatsConn != nil {
		a.nc = a.Options.NatsConn
		slog.Info("Node utilizing injected NATS connection", "id", a.ID)
	} else {
		nc, err := nats.Connect(a.Options.NatsURL)
		if err != nil {
			return fmt.Errorf("failed to connect to NATS: %w", err)
		}
		a.nc = nc
		defer a.nc.Close()
	}

	// Process any groups joined before Run() was called
	a.mu.Lock()
	for group, queue := range a.pendingGroup {
		if queue == "" {
			topic := a.Options.GroupPrefix + group
			sub, err := a.nc.Subscribe(topic, a.handleMessage)
			if err == nil {
				a.groups[group] = sub
				slog.Info("Node joined broadcast group", "id", a.ID, "group", group)
			}
		} else {
			topic := a.Options.QueuePrefix + group
			key := group + "::" + queue
			sub, err := a.nc.QueueSubscribe(topic, queue, a.handleMessage)
			if err == nil {
				a.groups[key] = sub
				slog.Info("Node joined queue group", "id", a.ID, "group", group, "queue", queue)
			}
		}
	}
	// Clear pending
	a.pendingGroup = make(map[string]string)
	a.mu.Unlock()

	// 1. Listen on private Inbox (P2P)
	inboxSub, err := a.nc.Subscribe(a.Options.InboxPrefix+a.ID+".inbox", a.handleMessage)
	if err != nil {
		return fmt.Errorf("failed to subscribe to inbox: %w", err)
	}
	defer func() { _ = inboxSub.Unsubscribe() }()

	// 2. Register with Control Plane (via the standard envelope, no raw bytes)
	regMsg := protocol.Message{
		ID:        fmt.Sprintf("reg-%d", time.Now().UnixNano()),
		Sender:    a.ID,
		Type:      protocol.TypeRegister,
		Timestamp: time.Now().Unix(),
	}
	if err := a.SendRaw(ctx, a.Options.RegisterSubject, regMsg); err != nil {
		slog.Error("Failed to send registration", "err", err)
	}

	// 3. Heartbeat loop
	ticker := time.NewTicker(a.Options.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Node shutting down", "id", a.ID)
			return nil
		case <-ticker.C:
			// Ping / Heartbeat can go here
		}
	}
}

// handleMessage is the universal receiver for all Message envelope types
func (a *Node) handleMessage(m *nats.Msg) {
	var msg protocol.Message
	if err := json.Unmarshal(m.Data, &msg); err != nil {
		slog.Error("Failed to unmarshal message", "err", err)
		return
	}

	slog.Debug("Node received message", "type", msg.Type.String(), "sender", msg.Sender)

	// Trigger the callback if configured
	if a.Options.OnMessage != nil {
		go a.Options.OnMessage(msg)
	}
}

// JoinGroup subscribes to a broadcast group (Fan-out mode)
func (a *Node) JoinGroup(ctx context.Context, group string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.groups[group]; exists {
		return nil
	}

	if a.nc == nil {
		a.pendingGroup[group] = ""
		return nil
	}

	topic := a.Options.GroupPrefix + group
	sub, err := a.nc.Subscribe(topic, a.handleMessage)
	if err != nil {
		return err
	}
	a.groups[group] = sub
	slog.Info("Node joined broadcast group", "id", a.ID, "group", group)
	return nil
}

// JoinQueueGroup subscribes to a task group (Load-Balanced mode)
func (a *Node) JoinQueueGroup(ctx context.Context, group string, queueName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	key := group + "::" + queueName
	if _, exists := a.groups[key]; exists {
		return nil
	}

	if a.nc == nil {
		a.pendingGroup[group] = queueName
		return nil
	}

	topic := a.Options.QueuePrefix + group
	sub, err := a.nc.QueueSubscribe(topic, queueName, a.handleMessage)
	if err != nil {
		return err
	}
	a.groups[key] = sub
	slog.Info("Node joined queue group", "id", a.ID, "group", group, "queue", queueName)
	return nil
}

// SendMessage sends a P2P message to another Node
func (a *Node) SendMessage(ctx context.Context, target string, payload []byte) error {
	msg := protocol.Message{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Sender:    a.ID,
		Target:    target,
		Type:      protocol.TypeP2P,
		Payload:   payload,
		Timestamp: time.Now().Unix(),
	}
	topic := a.Options.InboxPrefix + target + ".inbox"
	return a.SendRaw(ctx, topic, msg)
}

// Observe publishes an observability event to ObserveSubject+<id>+"."+kind (e.g.
// sys.obs.chat-agent.trace). It is fire-and-forget over core NATS: with no
// subscriber the message is simply discarded by the broker, so emitting costs a
// marshal + a local publish and nothing downstream. Debug consumers (a chat
// /trace subscription, or `plexus watch`) subscribe by wildcard when they want
// it. kind is e.g. "trace" / "raw" / "log" / "deleg".
func (a *Node) Observe(ctx context.Context, kind string, content []byte) error {
	msg := protocol.Message{
		ID:        fmt.Sprintf("obs-%d", time.Now().UnixNano()),
		Sender:    a.ID,
		Payload:   content,
		Timestamp: time.Now().Unix(),
	}
	return a.SendRaw(ctx, a.Options.ObserveSubject+a.ID+"."+kind, msg)
}

// SendRaw marshals a Message and publishes it to a raw NATS subject
func (a *Node) SendRaw(ctx context.Context, subject string, msg protocol.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.nc == nil {
		return fmt.Errorf("node not connected")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return a.nc.Publish(subject, data)
}
