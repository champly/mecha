package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

func newWebhookCmd() *cobra.Command {
	var agentID, port string

	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Forward a Claude Code hook event to the mecha server",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("webhook: read stdin: %w", err)
			}

			url := fmt.Sprintf("http://127.0.0.1:%s/webhook/%s", port, agentID)
			resp, err := http.Post(url, "application/json", bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("webhook: post to %s: %w", url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				return fmt.Errorf("webhook: server returned %d: %s", resp.StatusCode, string(body))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agentID, "id", "", "Agent ID")
	cmd.Flags().StringVar(&port, "port", "", "Server port")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}
