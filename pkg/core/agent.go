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

func (c *Core) createAgent(roleName string) (types.Agent, error) {
	a, err := agent.New(c.workspace, roleName, c.cfg)
	if err != nil {
		return nil, fmt.Errorf("core: create agent %q: %w", roleName, err)
	}
	if err := a.Prepare(); err != nil {
		return nil, fmt.Errorf("core: prepare agent %q: %w", roleName, err)
	}
	c.agentByID[a.ID()] = a
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

	inst.status = StatusBusy
	inst.result = make(chan taskResult, 1)

	c.logger.Info("dispatching task", "role", roleName, "task", task)
	if err := c.backend.Send(ctx, inst.handle, task+"\n"); err != nil {
		inst.status = StatusRunning
		return taskResult{}, fmt.Errorf("core: send task to %q: %w", roleName, err)
	}

	select {
	case result := <-inst.result:
		inst.status = StatusRunning
		return result, nil
	case <-ctx.Done():
		inst.status = StatusRunning
		return taskResult{}, ctx.Err()
	}
}

func (c *Core) ensureSpecialist(ctx context.Context, roleName string) (*instance, error) {
	if inst := c.specialists[roleName]; inst != nil {
		return inst, nil
	}

	a, err := c.createAgent(roleName)
	if err != nil {
		return nil, err
	}

	cmd := a.Cmd()
	c.logger.Info("starting agent", "role", roleName, "args", cmd.Args)

	handle, err := c.backend.Spawn(ctx, term.PaneSpec{
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
		status: StatusStarting,
		ready:  make(chan struct{}),
	}
	c.specialists[roleName] = inst
	c.instanceByAgentID[a.ID()] = inst

	c.logger.Info("agent spawned, waiting for SessionStart", "role", roleName)

	select {
	case <-inst.ready:
		inst.status = StatusRunning
		c.logger.Info("agent ready", "role", roleName)
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("core: agent %q start timeout", roleName)
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return inst, nil
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
