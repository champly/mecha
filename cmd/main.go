package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "mecha",
	Short: "Single-process multi-role orchestrator",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMecha()
	},
}

func main() {
	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newWebhookCmd())
	rootCmd.AddCommand(newAskCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
