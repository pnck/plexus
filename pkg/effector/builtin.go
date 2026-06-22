package effector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// This file provides 1-2 built-in effectors so downstream stages (E2.2/E2.4) can
// dispatch something real without a live MCP server, and so the policy/envelope
// layers have concrete effectors to test against.

// readFileEffector reads a file from disk. It is a Read effector (no side
// effects), so it is auto-allowed and lives inside the delegation envelope.
type readFileEffector struct{}

// ReadFile returns the built-in read_file effector (RiskTag Read).
func ReadFile() Effector { return readFileEffector{} }

func (readFileEffector) Name() string        { return "read_file" }
func (readFileEffector) Description() string { return "Read the contents of a file at the given path." }
func (readFileEffector) Risk() RiskTag       { return Read }

func (readFileEffector) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Filesystem path of the file to read." }
  },
  "required": ["path"],
  "additionalProperties": false
}`)
}

func (readFileEffector) Invoke(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	if in.Path == "" {
		return Result{Content: "missing required argument: path", IsError: true}, nil
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		// File-not-found / permission errors are tool-level errors fed back to
		// the model for self-correction, not infrastructure failures.
		return Result{Content: fmt.Sprintf("read_file failed: %v", err), IsError: true}, nil
	}
	return Result{Content: string(data)}, nil
}

// runCommandEffector runs a generic, arbitrary command. It is an ExecArbitrary
// effector (unbounded effect — generic shell, network, deploy), so the default
// policy routes it through approval and it is EXCLUDED from the delegation
// envelope. (A contained build/test variant carries the ExecSandboxed tag
// instead, which is approval-free and so is INCLUDED in the envelope — no
// name-matching needed.)
type runCommandEffector struct{}

// RunCommand returns the built-in run_command effector (RiskTag ExecArbitrary).
func RunCommand() Effector { return runCommandEffector{} }

func (runCommandEffector) Name() string { return "run_command" }
func (runCommandEffector) Description() string {
	return "Execute a command and return its combined stdout/stderr."
}
func (runCommandEffector) Risk() RiskTag { return ExecArbitrary }

func (runCommandEffector) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "Executable to run." },
    "args":    { "type": "array", "items": { "type": "string" }, "description": "Arguments to the command." }
  },
  "required": ["command"],
  "additionalProperties": false
}`)
}

func (runCommandEffector) Invoke(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	if in.Command == "" {
		return Result{Content: "missing required argument: command", IsError: true}, nil
	}
	cmd := exec.CommandContext(ctx, in.Command, in.Args...) //nolint:gosec // exec is this effector's purpose; sandboxing is an outer concern (bwrap)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A non-zero exit is a tool-level error: return output plus error for the
		// model to inspect and self-correct.
		return Result{Content: fmt.Sprintf("%s\ncommand failed: %v", out, err), IsError: true}, nil
	}
	return Result{Content: string(out)}, nil
}
