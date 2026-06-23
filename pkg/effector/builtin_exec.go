package effector

import (
	"context"
	"os/exec"
)

// Built-in process-execution primitives (E2.7). Execution is its own domain and
// its own risk axis: run_command is ExecArbitrary (approval-gated, excluded from
// the delegation envelope), and a future contained build/test effector would
// carry ExecSandboxed (approval-free, in-envelope) — the tag, not a name, draws
// the line (§5.7.4).

type runCommandArgs struct {
	Command string   `json:"command" desc:"Executable to run."`
	Args    []string `json:"args,omitempty" desc:"Arguments to the command."`
}

// RunCommand returns the built-in run_command effector (RiskTag ExecArbitrary).
// Unbounded effect (generic shell, network, deploy), so the default policy
// routes it through approval and the delegation envelope excludes it.
func RunCommand() Effector {
	return define(spec{
		Name: "run_command",
		Desc: "Execute a command and return its combined stdout/stderr.",
		Risk: ExecArbitrary,
	}, func(ctx context.Context, in runCommandArgs) (Result, error) {
		if in.Command == "" {
			return toolErr("missing required argument: command"), nil
		}
		cmd := exec.CommandContext(ctx, in.Command, in.Args...) //nolint:gosec // exec is this effector's purpose; sandboxing is an outer concern (bwrap)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return toolErr("%s\ncommand failed: %v", out, err), nil
		}
		return Result{Content: string(out)}, nil
	})
}
