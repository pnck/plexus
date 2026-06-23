package effector

import "plexus/pkg/store"

// BuiltinOptions configures which built-in effectors (E2.7) are assembled.
type BuiltinOptions struct {
	// WorkingMemory backs the mem_* effectors. When nil, mem_* are omitted (the
	// ltm_* stubs are always present, as is the rest of the primitive set).
	WorkingMemory *store.WorkingMemoryStore
	// Checkpoints backs the step_* checkpoint primitives (§5.7.9/§5.7.10). When
	// nil, step_* are omitted.
	Checkpoints *store.CheckpointStore
	// IncludeRunCommand adds run_command (ExecArbitrary, approval-gated). Off by
	// default: the approval-free primitives cover ordinary file/sys work, and
	// arbitrary shell is opt-in.
	IncludeRunCommand bool
}

// Builtins returns the built-in effector set for the given options. The
// file/sys primitives are always included (all Read/Write, approval-free, inside
// the delegation envelope). mem_* are included when a WorkingMemory store is
// supplied; ltm_* stubs and (optionally) run_command round out the set.
func Builtins(opts BuiltinOptions) []Effector {
	effs := []Effector{
		// filesystem (builtin.go + builtin_fs.go)
		ReadFile(), Stat(), ListDir(), Glob(), Search(),
		WriteFile(), EditFile(), MakeDir(), MoveFile(), RemoveFile(),
		// environment / time / cwd (builtin_sys.go)
		GetEnv(), Now(), GetCwd(),
		// long-term memory stubs (builtin_mem.go) — agent-private
		LtmGet(), LtmPut(),
	}
	if opts.WorkingMemory != nil {
		effs = append(effs, MemRead(opts.WorkingMemory), MemWrite(opts.WorkingMemory))
	}
	if opts.Checkpoints != nil {
		effs = append(effs,
			StepAdd(opts.Checkpoints), StepStart(opts.Checkpoints), StepComplete(opts.Checkpoints),
			StepBlock(opts.Checkpoints), StepList(opts.Checkpoints))
	}
	if opts.IncludeRunCommand {
		effs = append(effs, RunCommand())
	}
	return effs
}

// RegisterBuiltins registers Builtins(opts) into r.
func RegisterBuiltins(r *Registry, opts BuiltinOptions) {
	for _, e := range Builtins(opts) {
		r.Register(e)
	}
}
