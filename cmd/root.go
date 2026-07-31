package cmd

import "github.com/spf13/cobra"

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "mecha",
		Short: "Single-process multi-role orchestrator",
		SilenceErrors: true,
		SilenceUsage:  true,
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
