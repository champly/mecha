package core

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/champly/mecha/pkg/agent/types"
)

func (c *Core) launchCoordinator(ctx context.Context, a types.Agent, srv *http.Server) error {
	cmd := a.Cmd()
	cmd.Env = append(os.Environ(), cmd.Env...)
	c.logger.Info("starting agent", "role", "coordinator", "args", cmd.Args)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("core: start coordinator: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		for range sigCh {
			if cmd.Process != nil {
				cmd.Process.Signal(os.Interrupt)
			}
		}
	}()

	waitErr := cmd.Wait()
	c.logger.Info("agent exited", "role", "coordinator", "err", waitErr)

	signal.Stop(sigCh)
	close(sigCh)

	// Shut down the HTTP server first so no new /ask requests can arrive
	// while we tear down specialist instances.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)

	// Cleanup: kill all specialist panes and cancel waiting Asks.
	for roleName, inst := range c.specialists {
		c.logger.Info("killing specialist", "role", roleName)
		if inst.result != nil {
			select {
			case inst.result <- taskResult{err: "coordinator exited"}:
			default:
				// The Ask goroutine already consumed the result (or will shortly).
			}
		}
		if err := c.backend.Kill(ctx, inst.handle); err != nil {
			c.logger.Error("kill specialist failed", "role", roleName, "err", err)
		}
	}
	c.specialists = make(map[string]*instance)
	c.instanceByAgentID = make(map[string]*instance)

	return waitErr
}
