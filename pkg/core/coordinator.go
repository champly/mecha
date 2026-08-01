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

	if err := c.backend.Label(roleName); err != nil {
		c.logger.Warn("label coordinator pane", "role", roleName, "err", err)
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

	// Never let the coordinator outlive Core: kill it when the context is
	// cancelled, and reap it on startup failure so it is never orphaned.
	stopKill := context.AfterFunc(ctx, func() {
		_ = cmd.Process.Kill()
	})
	defer stopKill()

	fail := func(err error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	if err := inst.waitRegistered(ctx); err != nil {
		return fail(err)
	}

	if err := inst.waitReady(ctx); err != nil {
		return fail(err)
	}
	c.logger.Info("coordinator ready", "role", roleName)

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		// A cancelled ctx is an intentional shutdown, not a failure.
		return nil
	}
	if waitErr != nil {
		c.logger.Warn("coordinator exited with error", "role", roleName, "err", waitErr)
	}
	return waitErr
}
