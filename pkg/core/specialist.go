package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/champly/mecha/pkg/term"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// roleLock returns the per-role spawn lock. Locks are never removed, so a
// concurrent ask always finds the same lock for the same role.
func (c *Core) roleLock(role string) *sync.Mutex {
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	m, ok := c.spawnLocks[role]
	if !ok {
		m = &sync.Mutex{}
		c.spawnLocks[role] = m
	}
	return m
}

// ensureSpecialist returns a healthy instance for role, respawning it when
// missing or unhealthy. Unknown roles and the coordinator role are rejected.
func (c *Core) ensureSpecialist(ctx context.Context, role string) (*instance, error) {
	if _, ok := c.findRole(role); !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("core: unknown role %q", role))
	}
	if role == c.coordinatorRole() {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("core: coordinator role %q does not accept tasks", role))
	}

	// Serialize lookup-destroy-respawn per role so concurrent asks for the
	// same role don't spawn duplicates; different roles spawn independently
	// (a slow spawn for one role must not stall every other role's ask).
	lock := c.roleLock(role)
	lock.Lock()
	defer lock.Unlock()

	if inst := c.registry.getByRole(role); inst != nil {
		if inst.state.Load() != int32(stateUnhealthy) {
			return inst, nil
		}
		c.destroy(inst)
	}

	inst := newInstance(uuid.NewString(), role)
	c.registry.add(inst)
	c.logger.Info("spawning specialist", "role", role, "id", inst.id)

	handle, err := c.backend.Spawn(ctx, term.Spec{
		WorkDir: c.workspace,
		Command: []string{c.mechaBinary, "agentd", "--id", inst.id, "--addr", c.addr},
	})
	if err != nil {
		c.destroy(inst)
		return nil, fmt.Errorf("core: spawn specialist %q: %w", role, err)
	}
	inst.setHandle(handle)

	if err := inst.waitRegistered(ctx); err != nil {
		c.destroy(inst)
		return nil, err
	}

	if err := inst.waitReady(ctx); err != nil {
		c.destroy(inst)
		return nil, err
	}

	return inst, nil
}

// destroy removes inst from the registry and kills its pane.
func (c *Core) destroy(inst *instance) {
	c.registry.remove(inst)

	pane := inst.pane()
	if pane == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), paneKillTimeout)
	defer cancel()
	if err := c.backend.Kill(ctx, pane); err != nil {
		c.logger.Warn("kill pane failed", "role", inst.role, "id", inst.id, "err", err)
	}
}
