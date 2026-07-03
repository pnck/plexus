package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"plexus/internal/logger"
)

// trunkURL normalizes a --trunk value (host:port, or a full URL) into the mesh
// transport URL the bus client needs. The user-facing flag speaks "trunk" — the
// backbone that carries the mesh's signals; the URL scheme is an internal transport
// detail, so a bare host:port is accepted and the scheme is filled in here.
func trunkURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "nats://" + addr
}

var debug bool

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "plexus",
	Short: "Plexus is a high-density agent execution engine",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logger.Setup(debug)
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug logging")
}
