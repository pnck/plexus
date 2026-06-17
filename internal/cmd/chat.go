package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive chat mode to verify LLM connectivity",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Chat stub: LLM interactive mode not yet implemented.")
	},
}

func init() {
	rootCmd.AddCommand(chatCmd)
}
