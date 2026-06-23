//go:build !nochat

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"plexus/internal/chat"
	"plexus/pkg/brain"
)

var (
	chatProvider  string
	chatModel     string
	chatSystem    string
	chatBaseURL   string
	chatDebug     bool
	chatNatsPort  int
	chatAllowExec bool
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
		gw, err := chat.ResolveGateway(chatProvider, chatModel, chatBaseURL, chatDebug).Build()
		if err != nil {
			return err
		}
		var roleCard brain.RoleCard
		if chatSystem != "" {
			roleCard = brain.RoleCard{SystemPrompt: chatSystem}
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return chat.Run(ctx, chat.RunConfig{
			Gateway:           gw,
			RoleCard:          roleCard,
			NatsPort:          chatNatsPort,
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
	chatCmd.Flags().IntVar(&chatNatsPort, "nats-port", 4222, "Embedded NATS port")
	chatCmd.Flags().BoolVar(&chatAllowExec, "allow-exec", false, "Enable the run_command effector (arbitrary shell; each call is approval-gated)")
}
