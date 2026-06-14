package main

import (
	"fmt"
	"os"

	"github.com/champly/mecha/pkg/config"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize mecha configuration",
		Long:  "Write the default config.yaml to ~/.mecha/config.yaml.",
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")

			path, err := config.InitConfig(force)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Config initialized at %s\n", path)
			return nil
		},
	}

	cmd.Flags().BoolP("force", "f", false, "Overwrite existing config without backup")
	return cmd
}
