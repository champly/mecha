package cmd

import "github.com/spf13/cobra"

// maxErrorBody limits how much of the server error response is read into memory.
const maxErrorBody = 1 << 20 // 1 MiB

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "mecha",
		Short: "Single-process multi-role orchestrator",
		// `mecha` without subcommand is equivalent to `mecha run`.
		RunE: func(c *cobra.Command, args []string) error {
			return runMecha()
		},
	}

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newWebhookCmd())
	rootCmd.AddCommand(newAgentdCmd())
	rootCmd.AddCommand(newAskCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}
