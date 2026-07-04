// Package caps is the structured capability-requirement layer for sandbox setup.
// Each privileged component declares the Linux capabilities it needs (Requirer —
// the capability analog of the Describe() pattern); the launcher visits every
// participant, unions their needs (Collect), and raises them ONCE at startup
// (Ensure), failing with a clear, actionable message when the operator has not
// granted one. Everything that a capability can satisfy is handled here so plexus
// can acquire it at launch; a required UID is identity, not a capability, and is
// passed in separately as a launch parameter.
package caps

import (
	"sort"
	"strconv"
	"strings"
)

// Cap is a Linux capability, by its stable kernel number (so this file stays
// GOOS-agnostic; the raise itself is Linux-only, in ensure_linux.go).
type Cap int

const (
	NetAdmin Cap = 12 // CAP_NET_ADMIN: netns/veth/route/nft config, IP_TRANSPARENT sockets
	NetRaw   Cap = 13 // CAP_NET_RAW: raw/packet sockets
	SysAdmin Cap = 21 // CAP_SYS_ADMIN: create + mount a network namespace
	BPF      Cap = 39 // CAP_BPF: cgroup-BPF per-process attribution (E4.6.3.2)
)

func (c Cap) String() string {
	switch c {
	case NetAdmin:
		return "CAP_NET_ADMIN"
	case NetRaw:
		return "CAP_NET_RAW"
	case SysAdmin:
		return "CAP_SYS_ADMIN"
	case BPF:
		return "CAP_BPF"
	default:
		return "CAP_" + strconv.Itoa(int(c))
	}
}

// Set is a structured, deduplicated set of capabilities.
type Set struct{ m map[Cap]struct{} }

// Of builds a Set from the given capabilities.
func Of(cs ...Cap) Set {
	s := Set{m: make(map[Cap]struct{}, len(cs))}
	for _, c := range cs {
		s.m[c] = struct{}{}
	}
	return s
}

// Union returns the combined requirements of s and o.
func (s Set) Union(o Set) Set {
	out := Of()
	for c := range s.m {
		out.m[c] = struct{}{}
	}
	for c := range o.m {
		out.m[c] = struct{}{}
	}
	return out
}

// Has reports membership.
func (s Set) Has(c Cap) bool { _, ok := s.m[c]; return ok }

// Empty reports whether no capability is required.
func (s Set) Empty() bool { return len(s.m) == 0 }

// List returns the capabilities in a stable (numeric) order.
func (s Set) List() []Cap {
	out := make([]Cap, 0, len(s.m))
	for c := range s.m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Describe renders the requirement for an operator / a log line, e.g.
// "CAP_NET_ADMIN, CAP_SYS_ADMIN" (or "none").
func (s Set) Describe() string {
	if s.Empty() {
		return "none"
	}
	names := make([]string, 0, len(s.m))
	for _, c := range s.List() {
		names = append(names, c.String())
	}
	return strings.Join(names, ", ")
}

// Requirer is implemented by any component that needs host capabilities to do its
// privileged setup — the capability analog of the Describe() pattern. The launcher
// visits each participant and unions their requirements with Collect.
type Requirer interface {
	RequiredCaps() Set
}

// Collect unions the capability requirements of every participant (visitor pattern),
// so the launcher can Ensure them all at once, up front. Nil requirers are skipped.
func Collect(rs ...Requirer) Set {
	out := Of()
	for _, r := range rs {
		if r != nil {
			out = out.Union(r.RequiredCaps())
		}
	}
	return out
}
