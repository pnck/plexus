package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Diagnostic tool to probe the NATS bus and mesh state",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Inspect stub: Mesh diagnostics not yet implemented.")
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}
