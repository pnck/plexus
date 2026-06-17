package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"plexus/agent"
	"plexus/sandbox"
	"plexus/sandbox/bwrap"
)

var (
	natsURL   string
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

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		slog.Info("Starting plexus daemon", "id", agentID, "sandboxed", sandboxed)
		a := agent.New(agentID, agent.WithNatsURL(natsURL))

		return a.Run(ctx)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&natsURL, "nats-url", "nats://127.0.0.1:4222", "NATS server URL to connect to")
	runCmd.Flags().StringVar(&agentID, "id", "agent-x", "Agent identity")
	runCmd.Flags().BoolVar(&sandboxed, "sandboxed", false, "Run the daemon inside a strict bwrap sandbox")
}
