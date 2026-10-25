package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/champly/mecha/pkg/agentd"
	"github.com/spf13/cobra"
)

func newAgentdCmd() *cobra.Command {
	var id, addr string
	var logStdout bool

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

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			d := agentd.New(agentd.Options{ID: id, CoreAddr: addr, Context: ctx, LogStdout: logStdout})
			if err := d.Start(); err != nil {
				// A signal during startup is an intentional shutdown, not a
				// failure: exit 0 instead of reporting a canceled RPC.
				if ctx.Err() != nil {
					return nil
				}
				return err
			}

			// On SIGINT/SIGTERM stop the whole agent process group and exit 0.
			go func() {
				<-ctx.Done()
				d.Stop()
			}()
			d.Wait()
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Agentd instance ID")
	cmd.Flags().StringVar(&addr, "addr", "", "Core address (host:port)")
	cmd.Flags().BoolVar(&logStdout, "log-stdout", false, "Write agentd logs to stdout instead of the workspace log file")
	return cmd
}
