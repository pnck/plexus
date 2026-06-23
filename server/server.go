// Package server provides the master SDK for interacting with the Plexus agent mesh.
// It allows applications to register agents, receive reports, and issue commands.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"plexus/protocol"
)

// Server is the Plexus SDK server object. It connects to an existing NATS cluster
// and provides governance and routing capabilities over the Agent mesh.
type Server struct {
	Options Options
	nc      *nats.Conn
	
	mu     sync.Mutex
	agents []string
}

// New creates a new Server SDK instance
func New(opts ...Option) *Server {
	options := DefaultOptions()
	for _, o := range opts {
		o(&options)
	}

	return &Server{
		Options: options,
	}
}

// Run connects the Server SDK to the NATS bus and begins listening for mesh events
func (s *Server) Run(ctx context.Context) error {
	if s.Options.NatsConn != nil {
		s.nc = s.Options.NatsConn
		slog.Info("Server SDK utilizing injected NATS connection")
	} else {
		nc, err := nats.Connect(s.Options.NatsURL)
		if err != nil {
			return fmt.Errorf("server failed to connect to nats: %w", err)
		}
		s.nc = nc
		defer s.nc.Close()
		slog.Info("Server SDK connected to NATS", "url", s.Options.NatsURL)
	}

	// Listen for agent registrations
	regSub, err := s.nc.Subscribe(s.Options.RegisterSubject, func(m *nats.Msg) {
		var reg protocol.Message
		if err := json.Unmarshal(m.Data, &reg); err != nil {
			slog.Error("Failed to unmarshal registration", "err", err)
			return
		}
		agentID := reg.Sender
		s.mu.Lock()
		s.agents = append(s.agents, agentID)
		s.mu.Unlock()
		slog.Info("Agent registered", "id", agentID)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to registration subject: %w", err)
	}
	defer func() { _ = regSub.Unsubscribe() }()

	// Listen for agent reports
	repSub, err := s.nc.Subscribe(s.Options.ReportSubject, func(m *nats.Msg) {
		if s.Options.OnReport != nil {
			var report protocol.Message
			if err := json.Unmarshal(m.Data, &report); err == nil {
				// Called synchronously on the subscription's dispatcher so reports
				// are delivered in publish order (streamed chat frames depend on it).
				// OnReport must not block — push to a buffered channel and return.
				s.Options.OnReport(report)
			} else {
				slog.Error("Failed to unmarshal report", "err", err)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to report subject: %w", err)
	}
	defer func() { _ = repSub.Unsubscribe() }()

	// Wait for context cancellation
	<-ctx.Done()
	slog.Info("Server SDK shutting down...")
	
	return nil
}

// StartEmbeddedNATS is a utility function to spin up an in-process NATS server for local dev/testing.
// Production environments should rely on a standalone NATS cluster.
func StartEmbeddedNATS(port int) (*natsserver.Server, error) {
	opts := &natsserver.Options{
		Host:   "127.0.0.1",
		Port:   port,
		NoLog:  true,
		NoSigs: true,
	}

	ns, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to configure embedded nats server: %w", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, fmt.Errorf("embedded nats server failed to start within timeout")
	}

	slog.Info("Started embedded NATS server", "port", port)
	return ns, nil
}

// GetRegisteredAgents returns a copy of the registered agents list
func (s *Server) GetRegisteredAgents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	copied := make([]string, len(s.agents))
	copy(copied, s.agents)
	return copied
}

// SendP2P sends a targeted point-to-point message
func (s *Server) SendP2P(ctx context.Context, agentID string, msg protocol.Message) error {
	msg.Target = agentID
	msg.Type = protocol.TypeP2P
	
	inbox := s.Options.InboxPrefix + agentID + ".inbox"
	return s.send(ctx, inbox, msg)
}

// SendGroupBroadcast sends a Fan-out replica message to all members of a group
func (s *Server) SendGroupBroadcast(ctx context.Context, group string, msg protocol.Message) error {
	msg.Target = group
	msg.Type = protocol.TypeBroadcast
	
	topic := s.Options.GroupPrefix + group
	return s.send(ctx, topic, msg)
}

// SendGroupTask sends a Load-Balanced message to a group (1 worker handles it)
func (s *Server) SendGroupTask(ctx context.Context, group string, msg protocol.Message) error {
	msg.Target = group
	msg.Type = protocol.TypeQueueTask
	
	topic := s.Options.QueuePrefix + group
	return s.send(ctx, topic, msg)
}

func (s *Server) send(ctx context.Context, subject string, msg protocol.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.nc == nil || !s.nc.IsConnected() {
		return fmt.Errorf("server not connected")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.nc.Publish(subject, data)
}
