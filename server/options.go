package server

import (
	"github.com/nats-io/nats.go"
	"plexus/protocol"
)

// Options contains configuration for the Plexus control plane server
type Options struct {
	NatsURL         string
	NatsConn        *nats.Conn
	InboxPrefix     string
	GroupPrefix     string
	QueuePrefix     string
	RegisterSubject string
	ReportSubject   string
	OnReport        func(protocol.Message) // Callback for async report handling
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
		OnReport:        nil,
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

// WithOnReport sets the callback for incoming reports
func WithOnReport(callback func(protocol.Message)) Option {
	return func(o *Options) {
		o.OnReport = callback
	}
}
