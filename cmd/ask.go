package cmd

import (
	"fmt"

	"github.com/champly/mecha/pkg/api"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newAskCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "ask <role> <task>",
		Short: "Send a task delegation to the mecha server (blocks until complete)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				return fmt.Errorf("ask: --addr is required")
			}

			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return fmt.Errorf("ask: dial %s: %w", addr, err)
			}
			defer conn.Close()

			resp, err := api.NewCoreClient(conn).Ask(cmd.Context(), &api.AskRequest{
				Role: args[0],
				Task: args[1],
			})
			if err != nil {
				return fmt.Errorf("ask: rpc failed: %w", err)
			}

			if !resp.GetSuccess() {
				return fmt.Errorf("ask: %s", resp.GetResult())
			}

			fmt.Fprint(cmd.OutOrStdout(), resp.GetResult())
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "Server address (host:port)")
	_ = cmd.MarkFlagRequired("addr")
	return cmd
}
