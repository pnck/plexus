// Package agent provides the node SDK for the Plexus mesh.
// It allows individual services to connect to the mesh, register themselves,
// join broadcast or queue groups, and exchange messages.
package agent

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

// Agent represents a Plexus mesh node
type Agent struct {
	ID      string
	Options Options
	nc      *nats.Conn

	mu           sync.Mutex
	groups       map[string]*nats.Subscription
	pendingGroup map[string]string // key: topic/group, value: queueName (empty if broadcast)
}

// New creates a new Agent instance
func New(id string, opts ...Option) *Agent {
	options := DefaultOptions()
	for _, o := range opts {
		o(&options)
	}

	return &Agent{
		ID:           id,
		Options:      options,
		groups:       make(map[string]*nats.Subscription),
		pendingGroup: make(map[string]string),
	}
}

// Run connects to NATS and starts listening for commands
func (a *Agent) Run(ctx context.Context) error {
	if a.Options.NatsConn != nil {
		a.nc = a.Options.NatsConn
		slog.Info("Agent utilizing injected NATS connection", "id", a.ID)
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
				slog.Info("Agent joined broadcast group", "id", a.ID, "group", group)
			}
		} else {
			topic := a.Options.QueuePrefix + group
			key := group + "::" + queue
			sub, err := a.nc.QueueSubscribe(topic, queue, a.handleMessage)
			if err == nil {
				a.groups[key] = sub
				slog.Info("Agent joined queue group", "id", a.ID, "group", group, "queue", queue)
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

	// 2. Register with Control Plane
	if err := a.nc.Publish(a.Options.RegisterSubject, []byte(a.ID)); err != nil {
		slog.Error("Failed to send registration", "err", err)
	}

	// 3. Heartbeat loop
	ticker := time.NewTicker(a.Options.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Agent shutting down", "id", a.ID)
			return nil
		case <-ticker.C:
			// Ping / Heartbeat can go here
		}
	}
}

// handleMessage is the universal receiver for all Message envelope types
func (a *Agent) handleMessage(m *nats.Msg) {
	var msg protocol.Message
	if err := json.Unmarshal(m.Data, &msg); err != nil {
		slog.Error("Failed to unmarshal message", "err", err)
		return
	}

	slog.Debug("Agent received message", "type", msg.Type.String(), "sender", msg.Sender)

	// Trigger the callback if configured
	if a.Options.OnMessage != nil {
		go a.Options.OnMessage(msg)
	}
}

// JoinGroup subscribes to a broadcast group (Fan-out mode)
func (a *Agent) JoinGroup(ctx context.Context, group string) error {
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
	slog.Info("Agent joined broadcast group", "id", a.ID, "group", group)
	return nil
}

// JoinQueueGroup subscribes to a task group (Load-Balanced mode)
func (a *Agent) JoinQueueGroup(ctx context.Context, group string, queueName string) error {
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
	slog.Info("Agent joined queue group", "id", a.ID, "group", group, "queue", queueName)
	return nil
}

// LeaveGroup unsubscribes from a group
func (a *Agent) LeaveGroup(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if sub, exists := a.groups[key]; exists {
		_ = sub.Unsubscribe()
		delete(a.groups, key)
	} else {
		delete(a.pendingGroup, key)
	}
	return nil
}

// SendMessage sends a P2P message to another Agent
func (a *Agent) SendMessage(ctx context.Context, target string, payload []byte) error {
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

// SendRaw marshals a Message and publishes it to a raw NATS subject
func (a *Agent) SendRaw(ctx context.Context, subject string, msg protocol.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.nc == nil {
		return fmt.Errorf("agent not connected")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return a.nc.Publish(subject, data)
}
