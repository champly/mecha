package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func newWebhookCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Forward an agent hook event to the local agentd webhook server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				return fmt.Errorf("webhook: --addr is required")
			}

			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("webhook: read stdin: %w", err)
			}

			resp, err := http.Post("http://"+addr+"/webhook", "application/json", bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("webhook: post %s: %w", addr, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
				return fmt.Errorf("webhook: server returned %s: %s", resp.Status, body)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "Agentd webhook address (host:port)")
	_ = cmd.MarkFlagRequired("addr")
	return cmd
}
