//go:build !nochat

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"plexus/internal/chat"
	"plexus/pkg/brain"
	"plexus/sandbox/egress"
)

var (
	chatProvider    string
	chatModel       string
	chatSystem      string
	chatBaseURL     string
	chatDebug       bool
	chatTrunkPort   int
	chatAllowExec   bool
	chatReasoning   string
	chatWithSandbox bool
	chatSandboxCfg  sandboxConfig
)

// chatCmd launches a fully assembled agent (brain + effector + delegation +
// memory + checkpoint) hosted on a self-started embedded NATS mesh, and a thin
// REPL that talks to it over the bus. The user is a control-plane peer and never
// touches the cognitive loop directly (E2.6). This is a single, non-persisted
// session — there is no save/resume and no session concept (plexus has none).
var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Chat with a fully assembled plexus agent over the mesh",
	RunE: func(cmd *cobra.Command, args []string) error {
		// --sandbox drives the full sandbox flow (preflight + caps + Phase-0 fence +
		// bwrap + self-confine) — the SAME entry every command uses; chat is not a
		// special "lighter" mode. The host phases exec away and never return here; only
		// the in-sandbox phase (post-confine) falls through to run the session.
		if chatWithSandbox {
			chatSandboxCfg.AgentID = "chat"
			if err := enterSandbox(&chatSandboxCfg); err != nil {
				return err
			}
		}

		// Serve the transparent egress proxy on the sockets Phase-0 handed down (no-op
		// when there is no netns fence, e.g. un-sandboxed chat).
		stopEgress, err := egress.ServeInherited()
		if err != nil {
			return fmt.Errorf("egress proxy: %w", err)
		}
		defer stopEgress()

		// A runtime-reconfigurable gateway: chat starts even without a key — the
		// user sets one in-session with /key (no startup failure).
		gw := chat.NewMutableGateway(chat.ResolveGateway(chatProvider, chatModel, chatBaseURL, chatReasoning, chatDebug))

		var roleCard brain.RoleCard
		if chatSystem != "" {
			// --system is a freeform prompt override: Guidance renders verbatim.
			roleCard = brain.RoleCard{Guidance: chatSystem}
		}

		// Only SIGTERM tears the session down. Ctrl-C (SIGINT) must NOT cancel the
		// workflow context — it resets the in-flight turn (handled in the REPL).
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()

		return chat.Run(ctx, chat.RunConfig{
			Gateway:           gw,
			RoleCard:          roleCard,
			NatsPort:          chatTrunkPort,
			IncludeRunCommand: chatAllowExec,
		}, os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(chatCmd)
	chatCmd.Flags().StringVar(&chatProvider, "provider", "", "LLM provider: openai | anthropic (env PLEXUS_LLM_PROVIDER; auto-detected from available key if unset)")
	chatCmd.Flags().StringVar(&chatModel, "model", "", "Model id (env PLEXUS_LLM_MODEL, provider-specific default)")
	chatCmd.Flags().StringVar(&chatSystem, "system", "", "Override the default chat role card's system prompt")
	chatCmd.Flags().StringVar(&chatBaseURL, "base-url", "", "Optional API base URL (env PLEXUS_LLM_BASE_URL)")
	chatCmd.Flags().BoolVar(&chatDebug, "debug-llm", false, "Print raw LLM request body + response status")
	chatCmd.Flags().StringVar(&chatReasoning, "reasoning", "", "Reasoning effort: minimal|low|medium|high|xhigh|max (mapped/clamped per provider; env PLEXUS_REASONING)")
	chatCmd.Flags().IntVar(&chatTrunkPort, "trunk-port", 0, "Pin the embedded trunk (mesh bus) to a port; 0 auto-assigns a free one (printed at startup)")
	chatCmd.Flags().BoolVar(&chatAllowExec, "allow-exec", false, "Enable the run_command effector (arbitrary shell; each call is approval-gated)")
	chatCmd.Flags().BoolVar(&chatWithSandbox, "sandbox", false, "Establish the full sandbox (fs/ns isolation + network fence + cgroup); flags below only tune it")
	addSandboxFlags(chatCmd.Flags(), &chatSandboxCfg)
}
