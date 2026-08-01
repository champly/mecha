package cmd

import "github.com/spf13/cobra"

// configPath overrides the default ~/.mecha/config.yaml for commands that
// load configuration (mecha run).
var configPath string

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
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to config file (default ~/.mecha/config.yaml)")

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newWebhookCmd())
	rootCmd.AddCommand(newAgentdCmd())
	rootCmd.AddCommand(newAskCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}
