package core

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/champly/mecha/pkg/agent/types"
)

func (c *Core) launchCoordinator(_ context.Context, a types.Agent, srv *http.Server) error {
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

	// Cleanup: kill all specialist panes and cancel waiting Asks.
	for roleName, inst := range c.specialists {
		c.logger.Info("killing specialist", "role", roleName)
		if inst.result != nil {
			inst.result <- taskResult{err: "coordinator exited"}
		}
		if err := c.backend.Kill(context.Background(), inst.handle); err != nil {
			c.logger.Error("kill specialist failed", "role", roleName, "err", err)
		}
	}
	c.specialists = make(map[string]*instance)
	c.instanceByAgentID = make(map[string]*instance)

	srv.Shutdown(context.Background())
	return waitErr
}
