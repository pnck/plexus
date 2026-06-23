package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"plexus/protocol"
)

var (
	watchNatsURL string
	watchKind    string
)

// watchCmd subscribes to the mesh's observability streams (sys.obs.<id>.<kind>)
// and prints them — the standalone monitor for the "special" channels that carry
// debug output (tool/delegation trace, raw LLM, logs) off the functional report
// channel. Run it alongside `plexus chat` (which embeds NATS on the default
// port) or against any mesh NATS. With no agent id it watches every agent.
var watchCmd = &cobra.Command{
	Use:   "watch [agent-id]",
	Short: "Watch agents' observability streams (sys.obs.*)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agent := "*"
		if len(args) == 1 && args[0] != "" {
			agent = args[0]
		}
		kind := ">"
		if watchKind != "" {
			kind = watchKind
		}
		subject := "sys.obs." + agent + "." + kind

		nc, err := nats.Connect(watchNatsURL)
		if err != nil {
			return fmt.Errorf("watch: connect %s: %w", watchNatsURL, err)
		}
		defer nc.Close()

		sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
			var msg protocol.Message
			if json.Unmarshal(m.Data, &msg) != nil {
				return
			}
			ts := time.Unix(msg.Timestamp, 0).Format("15:04:05")
			fmt.Printf("\033[2m%s\033[0m \033[36m%s\033[0m %s\n", ts, m.Subject, string(msg.Payload))
		})
		if err != nil {
			return fmt.Errorf("watch: subscribe %q: %w", subject, err)
		}
		defer func() { _ = sub.Unsubscribe() }()

		fmt.Fprintf(os.Stderr, "watching %s on %s … (Ctrl-C to stop)\n", subject, watchNatsURL)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.Flags().StringVar(&watchNatsURL, "nats-url", "nats://127.0.0.1:4222", "NATS URL of the mesh to watch")
	watchCmd.Flags().StringVar(&watchKind, "kind", "", "Filter to one obs kind (trace|raw|deleg|thinking|log); default all")
}
