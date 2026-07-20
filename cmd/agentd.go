package cmd

import (
	"fmt"

	"github.com/champly/mecha/pkg/agentd"
	"github.com/spf13/cobra"
)

func newAgentdCmd() *cobra.Command {
	var id, addr string

	cmd := &cobra.Command{
		Use:   "agentd",
		Short: "Run an agent daemon managing a single agent process",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("agentd: --id is required")
			}
			if addr == "" {
				return fmt.Errorf("agentd: --addr is required")
			}

			d := agentd.New(agentd.Options{ID: id, CoreAddr: addr})
			if err := d.Start(); err != nil {
				return err
			}
			d.Wait()
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Agentd instance ID")
	cmd.Flags().StringVar(&addr, "addr", "", "Core address (host:port)")
	return cmd
}
