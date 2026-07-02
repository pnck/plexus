package bwrap

// Default sandbox-path convention (E4.4, provision face). These are DEFAULTS used
// when a Provision Bind leaves Dest empty; every one can be overridden per
// deployment by setting the Bind's Dest — the paths are not hardcoded law, a
// different base image can place things elsewhere.
const (
	RoleCardPath = "/plexus/role.yaml" // role card, READ-ONLY (an agent must not rewrite its own authority)
	StateDir     = "/plexus/state"     // brain-private SQLite (Checkpoint + WorkingMemory), read-write
	WorkspaceDir = "/work"             // the working tree, read-write; also the chdir target
	HomeDir      = "/home/plexus"      // writable HOME; user-space tools install here
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
}

// AddTo folds the provision mounts into p at each Bind's Dest (defaulting to the
// convention when Dest is empty). Additive: the launcher composes confine +
// provision + ambient into one Policy and enforces it (E4.6). The role card is
// read-only; the workspace sets Chdir; the home sets HOME.
func (pv Provision) AddTo(p *Policy) {
	if pv.RoleCard.Src != "" {
		p.ROBinds = append(p.ROBinds, withDest(pv.RoleCard, RoleCardPath))
	}
	if pv.State.Src != "" {
		p.Binds = append(p.Binds, withDest(pv.State, StateDir))
	}
	if pv.Workspace.Src != "" {
		b := withDest(pv.Workspace, WorkspaceDir)
		p.Binds = append(p.Binds, b)
		p.Chdir = b.Dest
	}
	if pv.Home.Src != "" {
		b := withDest(pv.Home, HomeDir)
		p.Binds = append(p.Binds, b)
		p.Setenv = append(p.Setenv, EnvVar{Key: "HOME", Value: b.Dest})
	}
}

// withDest returns b with its Dest defaulted to def when empty.
func withDest(b Bind, def string) Bind {
	if b.Dest == "" {
		b.Dest = def
	}
	return b
}
