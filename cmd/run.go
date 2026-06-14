package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/core"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start mecha and launch the coordinator",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMecha()
		},
	}
}

func runMecha() error {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	c, err := core.New(workspace, cfg)
	if err != nil {
		return fmt.Errorf("create core: %w", err)
	}
	return c.Start(ctx)
}
