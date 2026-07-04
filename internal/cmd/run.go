package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"plexus/pkg/agent"
	"plexus/pkg/brain"
	"plexus/pkg/mesh"
	"plexus/protocol"
	"plexus/sandbox"
	"plexus/sandbox/bwrap"
	"plexus/sandbox/egress"
	"plexus/sandbox/netpol"
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
		provider, err := bwrap.ProviderFromEnv()
		if err != nil {
			return err
		}
		if err := sandbox.EnterIfRequested(sandboxed, provider, nil); err != nil {
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

		return runNode(ctx)
	},
}

// runNode connects to the trunk and — when an LLM gateway is configured — hosts a
// fully assembled agent (brain + effectors + memory) driven off bus messages, the
// Phase-2 runtime (flow doc §4): it reads the role card from the provisioned path
// and drives Brain.Handle on each incoming message. Without a gateway it stays a
// bare mesh node (unchanged) so the daemon still runs for brain-less mesh work.
func runNode(ctx context.Context) error {
	gw, gwErr := agent.ResolveGateway("", "", "", "", false).Build()
	if gwErr != nil {
		slog.Warn("run: no LLM gateway configured — running as a bare mesh node", "err", gwErr)
		node := mesh.NewNode(agentID, mesh.WithNatsURL(trunkURL(trunkAddr)))
		slog.Info("Starting plexus daemon", "id", agentID, "sandboxed", sandboxed)
		return node.Run(ctx)
	}

	// Role card from the provisioned (read-only) path — the runtime side of E4.4; a
	// zero card is fine when unprovisioned (dev).
	roleCard, err := brain.LoadRoleCard(bwrap.RoleCardPath)
	if err != nil {
		slog.Info("run: no provisioned role card, using the zero card", "path", bwrap.RoleCardPath, "err", err)
	}

	ag, err := agent.New(ctx, agent.Config{
		Gateway:  gw,
		DBPath:   brainDBPath(agentID),
		RoleCard: roleCard,
		EnvState: sandboxEnvState(),
		// run_command stays off until the approval/yield path is wired for headless
		// agents (E5); the approval-free builtins cover ordinary work.
	})
	if err != nil {
		return fmt.Errorf("run: assemble agent: %w", err)
	}
	defer ag.Close()

	// The mesh delivers each message on its OWN goroutine, but a *brain.Brain is a
	// single-conversation actor with no internal locking — driving it concurrently
	// would race its history/current-task. So the callback only enqueues; one worker
	// goroutine drains the queue and drives Brain.Handle SERIALLY. (Per-TaskID
	// isolation — a distinct brain/history per task — is a later concern, E5.)
	inbox := make(chan protocol.Message, 64)
	var node *mesh.Node
	node = mesh.NewNode(agentID,
		mesh.WithNatsURL(trunkURL(trunkAddr)),
		mesh.WithOnMessage(func(msg protocol.Message) {
			select {
			case inbox <- msg:
			case <-ctx.Done():
			}
		}),
	)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-inbox:
				reply, herr := ag.Brain.Handle(ctx, msg)
				if herr != nil {
					slog.Error("run: brain handle", "err", herr)
					continue
				}
				_ = node.SendRaw(ctx, "sys.report", protocol.Message{
					Sender:  agentID,
					Target:  msg.Sender,
					Type:    protocol.TypeReport,
					TaskID:  msg.TaskID,
					Payload: []byte(reply),
				})
			}
		}
	}()
	slog.Info("Starting plexus agent", "id", agentID, "sandboxed", sandboxed)
	return node.Run(ctx)
}

// sandboxEnvState renders the sandbox environment-state L1 frame for a provisioned
// agent (fs view + network limits + resource ceilings), so the brain can tell the
// LLM its concrete constraints up front. It reads the same startup env Setup set;
// when there is no provisioned Policy (un-sandboxed dev run) it returns "" so no
// frame is injected.
func sandboxEnvState() string {
	js := os.Getenv(bwrap.EnvPolicy)
	if js == "" {
		return ""
	}
	var pol bwrap.Policy
	if err := json.Unmarshal([]byte(js), &pol); err != nil {
		return ""
	}
	env := sandbox.Environment{Policy: pol, Limits: sandbox.DefaultConfinement().Rlimits}
	if tcp := os.Getenv(egress.EnvNetTCP); tcp != "" {
		np := netpol.NetPolicy{TCP: parseNetAction(tcp), UDP: parseNetAction(os.Getenv(egress.EnvNetUDP))}
		env.Net = &np
	}
	return env.Describe()
}

// brainDBPath is the brain-private SQLite path for a run agent: the provisioned
// writable state dir inside the sandbox, or a per-agent temp path in dev.
func brainDBPath(id string) string {
	if fi, err := os.Stat(bwrap.StateDir); err == nil && fi.IsDir() {
		return filepath.Join(bwrap.StateDir, "brain.db")
	}
	dir := filepath.Join(os.TempDir(), "plexus-run", id)
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "brain.db")
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&trunkAddr, "trunk", "127.0.0.1:4222", "Trunk (mesh bus) address to connect to, host:port")
	runCmd.Flags().StringVar(&agentID, "id", "agent-x", "Agent identity")
	runCmd.Flags().BoolVar(&sandboxed, "sandbox", false, "Run the daemon inside a strict bwrap sandbox")
}
