package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"plexus/pkg/mesh"
	"plexus/sandbox"
	"plexus/sandbox/bwrap"
	"plexus/sandbox/egress"
)

var (
	trunkAddr string
	agentID   string
	sandboxed bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the plexus mesh daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := sandbox.EnterIfRequested(sandboxed, bwrap.New(), nil); err != nil {
			return err
		}

		// Inside a per-agent netns, serve the transparent egress proxy on the sockets
		// Setup handed down (no-op when there is no netns fence, e.g. dev/chat).
		stopEgress, err := egress.ServeInherited()
		if err != nil {
			return fmt.Errorf("egress proxy: %w", err)
		}
		defer stopEgress()

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		slog.Info("Starting plexus daemon", "id", agentID, "sandboxed", sandboxed)
		a := mesh.NewNode(agentID, mesh.WithNatsURL(trunkURL(trunkAddr)))

		return a.Run(ctx)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&trunkAddr, "trunk", "127.0.0.1:4222", "Trunk (mesh bus) address to connect to, host:port")
	runCmd.Flags().StringVar(&agentID, "id", "agent-x", "Agent identity")
	runCmd.Flags().BoolVar(&sandboxed, "sandbox", false, "Run the daemon inside a strict bwrap sandbox")
}
