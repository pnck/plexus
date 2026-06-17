package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"plexus/internal/logger"
)

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
