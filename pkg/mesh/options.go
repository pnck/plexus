package mesh

import (
	"time"

	"github.com/nats-io/nats.go"
	"plexus/protocol"
)

// Options contains configuration for the Plexus agent
type Options struct {
	NatsURL         string
	NatsConn        *nats.Conn // Allow injecting an existing connection
	InboxPrefix     string
	GroupPrefix     string
	QueuePrefix     string
	RegisterSubject string
	ReportSubject   string
	PingInterval    time.Duration
	OnMessage       func(protocol.Message) // Callback for async message handling
}

// Option is a functional option pattern
type Option func(*Options)

// DefaultOptions returns the system default options
func DefaultOptions() Options {
	return Options{
		NatsURL:         "nats://127.0.0.1:4222",
		NatsConn:        nil,
		InboxPrefix:     "agent.",
		GroupPrefix:     "group.broadcast.",
		QueuePrefix:     "group.queue.",
		RegisterSubject: "sys.register",
		ReportSubject:   "sys.report",
		PingInterval:    5 * time.Second,
		OnMessage:       nil,
	}
}

// WithNatsURL sets the NATS connection URL
func WithNatsURL(url string) Option {
	return func(o *Options) {
		o.NatsURL = url
	}
}

// WithNATSConn allows injecting a pre-configured NATS connection
func WithNATSConn(nc *nats.Conn) Option {
	return func(o *Options) {
		o.NatsConn = nc
	}
}

// WithOnMessage sets the callback for incoming messages
func WithOnMessage(callback func(protocol.Message)) Option {
	return func(o *Options) {
		o.OnMessage = callback
	}
}
