package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newAskCmd() *cobra.Command {
	var port string

	cmd := &cobra.Command{
		Use:   "ask <role> <task>",
		Short: "Send a task delegation to the mecha server (blocks until complete)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]string{
				"role": args[0],
				"task": args[1],
			}
			data, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("ask: marshal body: %w", err)
			}

			url := fmt.Sprintf("http://127.0.0.1:%s/ask", port)
			resp, err := http.Post(url, "application/json", bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("ask: post to %s: %w", url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				fmt.Fprintf(os.Stderr, "%s\n", errBody)
				os.Exit(1)
			}

			io.Copy(os.Stdout, resp.Body)
			return nil
		},
	}

	cmd.Flags().StringVar(&port, "port", "", "Server port")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}
