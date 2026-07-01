package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/champly/mecha/pkg/agent"
	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/term"
)

const (
	agentStartTimeout  = 30 * time.Second
	defaultTaskTimeout = 30 * time.Minute
)

func (c *Core) createAgent(roleName string) (types.Agent, error) {
	a, err := agent.New(c.workspace, roleName, c.cfg, c.runtime)
	if err != nil {
		return nil, fmt.Errorf("core: create agent %q: %w", roleName, err)
	}
	if err := a.Prepare(); err != nil {
		return nil, fmt.Errorf("core: prepare agent %q: %w", roleName, err)
	}
	c.mu.Lock()
	c.agentByID[a.ID()] = a
	c.mu.Unlock()
	return a, nil
}

// Ask dispatches a task to the named role and blocks until the task completes.
// If the specialist is not yet running it is spawned first.
func (c *Core) Ask(ctx context.Context, roleName, task string) (taskResult, error) {
	inst, err := c.ensureSpecialist(ctx, roleName)
	if err != nil {
		return taskResult{}, err
	}

	if task == "" {
		return taskResult{}, nil
	}

	ch := make(chan taskResult, 1)
	inst.result.Store(&ch)
	inst.status.Store(statusBusy)

	c.logger.Info("dispatching task", "role", roleName, "task", task)
	if err := c.backend.Send(ctx, inst.handle, task+"\n"); err != nil {
		inst.status.Store(statusRunning)
		inst.result.Store(nil)
		return taskResult{}, fmt.Errorf("core: send task to %q: %w", roleName, err)
	}

	select {
	case result := <-ch:
		inst.result.Store(nil) // prevent cleanup from sending a stale result
		inst.status.Store(statusRunning)
		return result, nil
	case <-time.After(defaultTaskTimeout):
		inst.result.Store(nil)
		c.logger.Warn("task timed out, killing specialist", "role", roleName)
		c.cleanupSpecialist(ctx, inst, roleName)
		return taskResult{}, fmt.Errorf("core: task %q timed out after %v", roleName, defaultTaskTimeout)
	case <-ctx.Done():
		inst.result.Store(nil)
		inst.status.Store(statusRunning)
		return taskResult{}, ctx.Err()
	}
}

func (c *Core) ensureSpecialist(ctx context.Context, roleName string) (*instance, error) {
	c.mu.Lock()
	if inst := c.specialists[roleName]; inst != nil {
		c.mu.Unlock()
		return inst, nil
	}
	c.mu.Unlock()

	a, err := c.createAgent(roleName)
	if err != nil {
		return nil, err
	}

	cmd := a.Cmd()
	c.logger.Info("starting agent", "role", roleName, "args", cmd.Args)

	handle, err := c.backend.Spawn(ctx, term.Spec{
		WorkDir: cmd.Dir,
		Command: cmd.Args,
		Env:     envToMap(cmd.Env),
	})
	if err != nil {
		return nil, fmt.Errorf("core: spawn %q: %w", roleName, err)
	}

	inst := &instance{
		role:   roleName,
		agent:  a,
		handle: handle,
		ready:  make(chan struct{}),
	}
	inst.status.Store(statusStarting)
	c.mu.Lock()
	c.specialists[roleName] = inst
	c.instanceByAgentID[a.ID()] = inst
	c.mu.Unlock()

	c.logger.Info("agent spawned, waiting for SessionStart", "role", roleName)

	select {
	case <-inst.ready:
		inst.status.Store(statusRunning)
		c.logger.Info("agent ready", "role", roleName)
		return inst, nil
	case <-time.After(agentStartTimeout):
		c.cleanupSpecialist(ctx, inst, roleName)
		return nil, fmt.Errorf("core: agent %q start timeout", roleName)
	case <-ctx.Done():
		c.cleanupSpecialist(ctx, inst, roleName)
		return nil, ctx.Err()
	}
}

// cleanupSpecialist kills a specialist pane and removes its tracking entries.
// The lock is only held for the map cleanup; the Kill call happens outside it
// since the instance pointer is already local and not shared after removal.
func (c *Core) cleanupSpecialist(ctx context.Context, inst *instance, roleName string) {
	c.logger.Warn("cleaning up specialist", "role", roleName)
	if err := c.backend.Kill(ctx, inst.handle); err != nil {
		c.logger.Error("kill specialist after timeout failed", "role", roleName, "err", err)
	}
	c.mu.Lock()
	delete(c.specialists, roleName)
	delete(c.instanceByAgentID, inst.agent.ID())
	c.mu.Unlock()
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
