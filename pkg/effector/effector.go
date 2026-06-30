// Package effector defines the tool ("effector") abstraction and the policy
// layer around it (§5.7.4). An effector is a single tool the brain — or a
// delegation — can invoke: a direct action with no context window of its own.
//
// This package provides four things:
//
//   - The Effector interface and its EffectSet vocabulary (observable-consequence
//     tags, one opaque set type — mono = singleton, compound = union, E3.1).
//   - A Registry of all effectors available to an agent's brain (built-in +
//     MCP-sourced).
//   - A Policy that decides which effectors require human approval, by a pure
//     subset test of an effector's effects against a role's permitted set (E3.2).
//   - The delegation capability envelope (能力封套): a mediated, filtered handle
//     handed to a delegation that exposes ONLY the approval-free subset and denies
//     out-of-envelope calls. Delegations never hold an MCP client or the Registry
//     directly; they reach tools only through this handle.
package effector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EffectSet is the one effect type (E3.1): a set of observable-consequence
// classes a tool call produces. A MONO effect is a singleton set (one bit); a
// COMPOUND/bundle is a union of monos — same type, one dimension. Gating is a
// single predicate, e.SubsetOf(permitted) (E3.2).
//
// An effect is a FIXED property of the tool keyed on its identity, NOT the
// caller's intent (D6): a tool whose effect the caller could change via free-form
// args carries ExecArbitrary (the unbounded escape hatch), never a finer tag. It
// is NOT a capability fence — a released arbitrary-exec shell can still do
// anything; the real fence is bwrap (E3.5/E4). The soft layer is authority/UX/audit.
//
// EffectSet is OPAQUE on purpose: callers compose via NewEffectSet/Union and test
// via SubsetOf/Contains, never by touching the backing word. The vocabulary is
// CLOSED and curated (D1) — atoms are added here by review, never minted by tools
// or MCP — so a uint64 bitmask (64 atoms) is ample; opacity keeps that ceiling a
// private detail, swappable to a wider backing without touching one call site.
type EffectSet struct{ bits uint64 }

// bit positions of the mono effects. Private — the public handles are the
// EffectSet singletons below. iota keeps them sequential and gives bitCount, which
// sizes the compile-time ceiling guard. Grouped by subsystem (dotted prefix); the
// grouping is organizational only, no structure or ordering is implied (D1).
const (
	bitFSRead uint = iota
	bitFSWrite
	bitFSSpecial

	bitExecBoxed
	bitExecArbitrary

	bitSecretRead
	bitSecretWrite

	bitNetAccess
	bitNetListen

	bitVCSRead
	bitVCSStage
	bitVCSCommit
	bitVCSReset
	bitVCSBranch
	bitVCSMerge
	bitVCSFetch
	bitVCSPush

	bitToolchainRead
	bitToolchainSetup
	bitToolchainSync
	bitToolchainUpgrade

	bitHostRead
	bitProcSpawn
	bitProcSignal

	bitMeshSpawn
	bitMeshSend

	bitTaskRead
	bitTaskReport
	bitTaskRevert
	bitTaskDecompose
	bitTaskRewrite
	bitTaskReorder

	bitCount // number of mono atoms; keep last
)

// Compile-time ceiling guard: the uint64 backing holds 64 atoms (bits 0..63). A
// 65th makes (64 - bitCount) a negative untyped constant, which cannot convert to
// uint, so the BUILD FAILS — a deliberate stop (widen the backing or prune the
// taxonomy) instead of a silent overflow. Encapsulation makes widening a local change.
const _ = uint(64 - bitCount)

// bitName / byName are the canonical-name registries. mono() fills both at the
// single declaration site, binding each name to its bit there — so there is no
// parallel table to drift and no completeness test to rely on. mono() panics on a
// reused bit or name, so a copy-paste slip fails at startup (every test run included).
var (
	bitName = make(map[uint]string)      // bit -> "fs.read", for String()
	byName  = make(map[string]EffectSet) // "fs.read" -> singleton, for ParseEffect
)

// mono declares a mono effect: it binds name to bit (the sole source of truth) and
// returns the singleton EffectSet. Called only from the var block below.
func mono(bit uint, name string) EffectSet {
	if _, dup := bitName[bit]; dup {
		panic(fmt.Sprintf("effector: bit %d reused declaring %q", bit, name))
	}
	if _, dup := byName[name]; dup {
		panic(fmt.Sprintf("effector: effect name %q declared twice", name))
	}
	s := EffectSet{bits: 1 << bit}
	bitName[bit] = name
	byName[name] = s
	return s
}

// The mono effects — the public vocabulary handles, each a singleton EffectSet.
// Reserved atoms (no tool carries them yet) are declared all the same: the set IS
// the closed vocabulary. Tools wire onto these in builtin_*.go / mcp_adapter.go.
var (
	// ── fs · workspace filesystem ──
	FSRead    = mono(bitFSRead, "fs.read")       // read/stat/list/glob/search the workspace
	FSWrite   = mono(bitFSWrite, "fs.write")     // write/edit/mkdir/move/remove regular files
	FSSpecial = mono(bitFSSpecial, "fs.special") // write a non-regular file (device/socket/fifo): a live kernel object

	// ── exec · code execution ──
	ExecBoxed     = mono(bitExecBoxed, "exec.boxed")         // run code CONTAINED to the sandbox: bounded, no escape
	ExecArbitrary = mono(bitExecArbitrary, "exec.arbitrary") // unbounded code (generic shell): consequence ≡ union of all effects; the escape hatch

	// ── secret · credentials ──
	SecretRead  = mono(bitSecretRead, "secret.read")   // read credentials (env/vault)
	SecretWrite = mono(bitSecretWrite, "secret.write") // inject/set credentials

	// ── net · network ──
	NetAccess = mono(bitNetAccess, "net.access") // initiate an outbound request (exfil/SSRF surface)
	NetListen = mono(bitNetListen, "net.listen") // accept inbound connections

	// ── vcs · version control ──
	VCSRead   = mono(bitVCSRead, "vcs.read")     // status/log/diff/show
	VCSStage  = mono(bitVCSStage, "vcs.stage")   // add/rm to the index (lowest consequence)
	VCSCommit = mono(bitVCSCommit, "vcs.commit") // create a commit (local history grows)
	VCSReset  = mono(bitVCSReset, "vcs.reset")   // reset/revert/checkout/restore (destructive-local)
	VCSBranch = mono(bitVCSBranch, "vcs.branch") // create/delete/switch branches & tags
	VCSMerge  = mono(bitVCSMerge, "vcs.merge")   // merge/rebase (rewrites local history)
	VCSFetch  = mono(bitVCSFetch, "vcs.fetch")   // fetch/pull (imports remote code, inbound)
	VCSPush   = mono(bitVCSPush, "vcs.push")     // push (writes the shared remote ≡ others' truth ≡ highest consequence)

	// ── toolchain · dependencies / toolchain ──
	ToolchainRead    = mono(bitToolchainRead, "toolchain.read")       // query installed versions
	ToolchainSetup   = mono(bitToolchainSetup, "toolchain.setup")     // install/initialize a toolchain
	ToolchainSync    = mono(bitToolchainSync, "toolchain.sync")       // sync to a lockfile (deterministic)
	ToolchainUpgrade = mono(bitToolchainUpgrade, "toolchain.upgrade") // change dependency versions (supply-chain surface)

	// ── host/proc · process & host metadata ──
	HostRead   = mono(bitHostRead, "host.read")     // now/get_cwd/hostname (lazy metainfo)
	ProcSpawn  = mono(bitProcSpawn, "proc.spawn")   // start a long-lived process/daemon
	ProcSignal = mono(bitProcSignal, "proc.signal") // kill/signal other processes

	// ── mesh · inter-agent ──
	MeshSpawn = mono(bitMeshSpawn, "mesh.spawn") // spawn a sub-cognition (delegate)
	MeshSend  = mono(bitMeshSend, "mesh.send")   // send a message to another agent

	// ── task · control-plane DAG ──
	TaskRead      = mono(bitTaskRead, "task.read")           // read the DAG/issue/task
	TaskReport    = mono(bitTaskReport, "task.report")       // report status truth (floor: always granted)
	TaskRevert    = mono(bitTaskRevert, "task.revert")       // request a reopen (floor: always granted)
	TaskDecompose = mono(bitTaskDecompose, "task.decompose") // break a node into sub-tasks (additive)
	TaskRewrite   = mono(bitTaskRewrite, "task.rewrite")     // rewrite a node's goal/spec (can invalidate in-flight work)
	TaskReorder   = mono(bitTaskReorder, "task.reorder")     // change dependencies/order (affects scheduling)
)

// Compound & bundle effects: composed from the monos above by union, so they are
// the SAME type (EffectSet) — the consistency the two-dimension split lacked. A
// COMPOUND is a tool's multi-effect consequence (net.download writes a file AND
// touches the network); a BUNDLE is a named role grant (task.manage, given only to
// the DAG manager). Both are gated by the same SubsetOf predicate; bundle
// expansion / the always-granted floor stay grant-side (E3.2/E5).
var (
	NetDownload = NewEffectSet(NetAccess, FSWrite)
	TaskManage  = NewEffectSet(TaskRead, TaskDecompose, TaskRewrite, TaskReorder)
)

// NewEffectSet returns the union of the given effects (∅ with no arguments). Used
// to declare a tool's effect set and to compose compounds/bundles.
func NewEffectSet(effs ...EffectSet) EffectSet {
	var s EffectSet
	for _, e := range effs {
		s.bits |= e.bits
	}
	return s
}

// ParseEffect parses a canonical dotted name into its singleton EffectSet. Unknown
// names error — the vocabulary is closed (MCP tools map onto it, never extend it).
func ParseEffect(name string) (EffectSet, error) {
	if s, ok := byName[name]; ok {
		return s, nil
	}
	return EffectSet{}, fmt.Errorf("unknown effect %q", name)
}

// ParseEffectSet parses canonical dotted names into their union. Empty/nil input
// yields ∅; an unknown name is an error.
func ParseEffectSet(names []string) (EffectSet, error) {
	var s EffectSet
	for _, n := range names {
		e, err := ParseEffect(n)
		if err != nil {
			return EffectSet{}, err
		}
		s = s.Union(e)
	}
	return s, nil
}

// Union returns s ∪ other.
func (s EffectSet) Union(other EffectSet) EffectSet { return EffectSet{s.bits | other.bits} }

// SubsetOf reports whether s ⊆ other — the gating predicate: a call is
// auto-allowed iff its effects are a subset of the role's permitted set (E3.2).
func (s EffectSet) SubsetOf(other EffectSet) bool { return s.bits&^other.bits == 0 }

// Contains reports whether other ⊆ s — membership of one or more effects.
func (s EffectSet) Contains(other EffectSet) bool { return other.SubsetOf(s) }

// IsEmpty reports whether s is ∅ (no world-consequence: internal cognition), which
// is ⊆ everything and so always auto-allowed.
func (s EffectSet) IsEmpty() bool { return s.bits == 0 }

// String renders the set as a comma-joined list of canonical dotted names in bit
// order (∅ for the empty set) — for logs, /tools and YAML round-tripping.
func (s EffectSet) String() string {
	if s.bits == 0 {
		return "∅"
	}
	var parts []string
	for bit := uint(0); bit < bitCount; bit++ {
		if s.bits&(1<<bit) != 0 {
			parts = append(parts, bitName[bit])
		}
	}
	return strings.Join(parts, ",")
}

// Result is the outcome of an effector invocation. IsError signals a tool-level
// error (the tool ran but failed) which is fed back to the LLM for
// self-correction, as opposed to an infrastructure error returned as a Go error
// from Invoke.
type Result struct {
	// Content is the textual result fed back into the model's context.
	Content string
	// IsError marks a tool-level error for LLM self-correction.
	IsError bool
}

// AgentPrivate is an optional interface an effector implements to opt OUT of the
// delegation capability envelope even when it is approval-free. It marks tools
// that belong to the agent's brain alone — memory (mem_*/ltm_*) is agent-private
// because a delegation has no persistent memory (§5.7.7): its job is to run a
// lean, stateless LLM↔tools loop and return a distilled Result. An effector that
// does not implement this interface (or returns false) is treated as shareable.
// AgentPrivate is ORTHOGONAL to a tool's effects: it governs delegation
// membership, not approval gating.
type AgentPrivate interface {
	// AgentPrivate reports whether this effector is excluded from delegations.
	AgentPrivate() bool
}

// isAgentPrivate reports whether e has opted out of the delegation envelope.
func isAgentPrivate(e Effector) bool {
	ap, ok := e.(AgentPrivate)
	return ok && ap.AgentPrivate()
}

// Effector is one tool the brain (or a delegation) can invoke. Implementations
// must be safe for concurrent use.
type Effector interface {
	// Name is the unique tool identifier surfaced to the LLM.
	Name() string
	// Description is a human-readable hint for the model.
	Description() string
	// Effects reports the observable-consequence set used by Policy (E3.1/E3.2).
	Effects() EffectSet
	// Schema is the JSON Schema object describing Invoke's arguments.
	Schema() json.RawMessage
	// Invoke runs the tool. A non-nil error is an infrastructure/transport
	// failure; a tool-level failure is reported via Result.IsError with a nil
	// error so it can be fed back to the model.
	Invoke(ctx context.Context, args json.RawMessage) (Result, error)
}
