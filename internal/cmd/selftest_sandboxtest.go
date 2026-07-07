//go:build linux && sandboxtest

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"plexus/internal/sandboxtest"
	"plexus/sandbox"
)

// selftestSandboxCfg tunes the sandbox the self-test enters. Its defaults are the full
// deny-all sandbox — the enforcement checks assert that deny-all actually blocks, so the
// command deliberately does NOT loosen the network policy.
var selftestSandboxCfg sandbox.Config

// selftestCmd is a hidden dev/CI command (compiled only with `-tags sandboxtest`): it
// walks the SAME sandbox entry chain (launch -> fence -> jail -> confine) and then, from
// inside the finished sandbox, asserts each isolation property actually holds. It is the
// enforcement counterpart to `run --sandbox` (which only proves the sandbox starts).
var selftestCmd = &cobra.Command{
	Use:    "sandbox-selftest",
	Short:  "Enter the sandbox and assert each isolation property holds (dev/CI only)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		selftestSandboxCfg.AgentID = sandboxtest.AgentID
		// The host-side stages exec away and never return; only the confined stage falls
		// through here, so past this point we are INSIDE the finished sandbox.
		if err := sandbox.Enter(selftestSandboxCfg); err != nil {
			return err
		}
		ok, results := sandboxtest.RunAll()
		sandboxtest.Report(os.Stdout, results)
		if !ok {
			return fmt.Errorf("sandbox self-test: one or more isolation checks FAILED")
		}
		fmt.Println("sandbox self-test: all enforcement checks passed")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(selftestCmd)
	addSandboxFlags(selftestCmd.Flags(), &selftestSandboxCfg)
}
