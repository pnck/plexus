package bwrap

import "strings"

// Default sandbox-path convention (E4.4, provision face). These are DEFAULTS used
// when a Provision Bind leaves Dest empty; every one can be overridden per
// deployment by setting the Bind's Dest — the paths are not hardcoded law, a
// different base image can place things elsewhere.
const (
	RoleCardPath   = "/plexus/role.yaml" // role card, READ-ONLY (an agent must not rewrite its own authority)
	StateDir       = "/plexus/state"     // brain-private SQLite (Checkpoint + WorkingMemory), read-write
	WorkspaceDir   = "/work"             // the working tree, read-write; also the chdir target
	HomeDir        = "/home/plexus"      // writable HOME; user-space tools install here
	ResolvConfPath = "/etc/resolv.conf"  // DNS config, READ-ONLY (DNS-over-TCP so udp:drop still resolves)
)

// Provision is what the launcher injects into an agent's sandbox: the role card
// (read-only), the brain-private state dir, the workspace, and a writable HOME
// (all read-write) — the "necessary cognition" an agent cannot self-produce
// (E4.1 §2b). Each field is a host->sandbox Bind: set Src to the host path; leave
// Dest empty to use the default-path convention above, or set Dest to place it
// elsewhere. Empty Src skips that mount. The HOST paths and their VALUES come from
// the E5 catalog; the SANDBOX paths are this (overridable) convention.
//
// This is the MODELING side. The runtime side — the sandboxed agent loading its
// role card from the role-card mount — rides with per-agent assembly in E4.6.
type Provision struct {
	RoleCard  Bind // read-only inject; empty Dest -> RoleCardPath
	State     Bind // read-write; empty Dest -> StateDir
	Workspace Bind // read-write + Chdir to its Dest; empty Dest -> WorkspaceDir
	// Home is read-write + a HOME env pointing at its Dest. rootfs-writability model:
	// the SYSTEM rootfs stays read-only/shared and an agent installs tools into its
	// own writable HOME (no per-agent overlay/CoW). Setting HOME is the
	// LANGUAGE-AGNOSTIC lever; user-space installers place tools under it. plexus sets
	// NOTHING toolchain-specific — PATH and per-toolchain bin dirs belong to the
	// curated base image. Mirrors Claude Code's sandbox: ro system + writable HOME +
	// user-space install. empty Dest -> HomeDir.
	Home Bind
	// ResolvConf is a read-only /etc/resolv.conf the launcher generated (DNS-over-TCP,
	// `options use-vc`, so a udp:drop role still resolves names — E4.6.6). empty Dest
	// -> ResolvConfPath.
	ResolvConf Bind
}

// args renders the provisioned mounts as bwrap arguments (Translate composes them
// with the read-only base and the invariants): role card --ro-bind (cannot rewrite
// its own authority), state/workspace/home --bind, plus --chdir (workspace) and the
// HOME env.
func (pv Provision) args() []string {
	var a []string
	if pv.RoleCard.Src != "" {
		b := withDest(pv.RoleCard, RoleCardPath)
		a = append(a, "--ro-bind", b.Src, b.Dest)
	}
	if pv.State.Src != "" {
		b := withDest(pv.State, StateDir)
		a = append(a, "--bind", b.Src, b.Dest)
	}
	if pv.Workspace.Src != "" {
		b := withDest(pv.Workspace, WorkspaceDir)
		a = append(a, "--bind", b.Src, b.Dest, "--chdir", b.Dest)
	}
	if pv.Home.Src != "" {
		b := withDest(pv.Home, HomeDir)
		a = append(a, "--bind", b.Src, b.Dest, "--setenv", "HOME", b.Dest)
	}
	if pv.ResolvConf.Src != "" {
		b := withDest(pv.ResolvConf, ResolvConfPath)
		a = append(a, "--ro-bind", b.Src, b.Dest)
	}
	return a
}

// writable lists the writable provisioned sandbox paths (state/workspace/home) for
// Policy.Describe.
func (pv Provision) writable() string {
	var ds []string
	if pv.State.Src != "" {
		ds = append(ds, destOf(pv.State, StateDir))
	}
	if pv.Workspace.Src != "" {
		ds = append(ds, destOf(pv.Workspace, WorkspaceDir))
	}
	if pv.Home.Src != "" {
		ds = append(ds, destOf(pv.Home, HomeDir))
	}
	return strings.Join(ds, ", ")
}

// workdir is the sandbox-side workspace path (the working directory), or "".
func (pv Provision) workdir() string {
	if pv.Workspace.Src == "" {
		return ""
	}
	return destOf(pv.Workspace, WorkspaceDir)
}

// roleCardDest is the sandbox-side role-card path (read-only), or "".
func (pv Provision) roleCardDest() string {
	if pv.RoleCard.Src == "" {
		return ""
	}
	return destOf(pv.RoleCard, RoleCardPath)
}

// withDest returns b with its Dest defaulted to def when empty.
func withDest(b Bind, def string) Bind {
	if b.Dest == "" {
		b.Dest = def
	}
	return b
}

// destOf is withDest(...).Dest.
func destOf(b Bind, def string) string { return withDest(b, def).Dest }
