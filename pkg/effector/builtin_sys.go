package effector

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Built-in environment/time/cwd primitives (E2.7). get_env reads credentials
// (SecretRead — env is the credential store); now/get_cwd expose only inert
// ambient host metadata (clock, working dir) with no gateable consequence, so
// they carry the empty effect set (∅) and are always auto-allowed.

type getEnvArgs struct {
	Name string `json:"name" desc:"Variable name."`
}

// GetEnv returns the built-in get_env effector (SecretRead: env is where
// credentials live, so it is gateable independently of plain fs reads).
func GetEnv() Effector {
	return define(spec{
		Name:    "get_env",
		Desc:    "Read an environment variable; reports it is not set when absent.",
		Effects: NewEffectSet(SecretRead),
	}, func(_ context.Context, in getEnvArgs) (Result, error) {
		if in.Name == "" {
			return toolErr("missing required argument: name"), nil
		}
		if val, ok := os.LookupEnv(in.Name); ok {
			return Result{Content: val}, nil
		}
		return Result{Content: fmt.Sprintf("%s is not set", in.Name)}, nil
	})
}

// Now returns the built-in now effector (∅: inert clock read): the current
// wall-clock time, so the model does not have to guess it.
func Now() Effector {
	return define(spec{
		Name: "now",
		Desc: "Current time, RFC3339 (UTC) plus Unix seconds.",
	}, func(_ context.Context, _ noArgs) (Result, error) {
		t := time.Now().UTC()
		return Result{Content: fmt.Sprintf("%s (unix %d)", t.Format(time.RFC3339), t.Unix())}, nil
	})
}

// GetCwd returns the built-in get_cwd effector (∅: inert working-dir read).
func GetCwd() Effector {
	return define(spec{
		Name: "get_cwd",
		Desc: "Report the current working directory.",
	}, func(_ context.Context, _ noArgs) (Result, error) {
		wd, err := os.Getwd()
		if err != nil {
			return toolErr("get_cwd failed: %v", err), nil
		}
		return Result{Content: wd}, nil
	})
}
