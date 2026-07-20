package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/google/uuid"
)

// launchCoordinator starts the coordinator agentd as a foreground child
// process, waits until it is ready, and blocks until it exits.
func (c *Core) launchCoordinator(ctx context.Context) error {
	roleName := c.coordinatorRole()
	if roleName == "" {
		return fmt.Errorf("core: no coordinator role found")
	}

	inst := newInstance(uuid.NewString(), roleName)
	c.registry.add(inst)

	c.logger.Info("launching coordinator", "role", roleName, "id", inst.id)

	cmd := exec.Command(c.mechaBinary, "agentd", "--id", inst.id, "--addr", c.addr)
	cmd.Dir = c.workspace

	// Attach the terminal: agentd relays the agent PTY to its own stdio.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("core: start coordinator: %w", err)
	}

	if err := inst.waitRegistered(ctx); err != nil {
		return err
	}

	if err := inst.waitReady(ctx); err != nil {
		return err
	}
	c.logger.Info("coordinator ready", "role", roleName)

	if err := cmd.Wait(); err != nil {
		c.logger.Info("coordinator exited with error", "err", err)
	}

	c.shutdown()
	return nil
}
