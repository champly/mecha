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

	// Phase 1: Shut down the HTTP server so no new /ask requests can arrive.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)

	// Phase 2-3: Notify busy specialists that the coordinator is exiting and wait up
	// to the configured grace period for in-flight tasks to complete naturally.
	c.drainSpecialists(ctx, c.shutdownGracePeriod)

	// Phase 4: Force-cleanup all remaining specialist panes and trackers.
	c.forceCleanup(ctx)

	return waitErr
}

// drainSpecialists sends a shutdown notification to all busy specialists and
// waits for the grace period to let in-flight tasks complete naturally.
// The lock is only held while collecting the busy instance list; I/O and sleep
// happen outside the lock so other goroutines are not blocked.
func (c *Core) drainSpecialists(ctx context.Context, gracePeriod time.Duration) {
	var busyInstances []*instance

	c.mu.Lock()
	for _, inst := range c.specialists {
		if inst.status.Load() == statusBusy {
			busyInstances = append(busyInstances, inst)
		}
	}
	c.mu.Unlock()

	if len(busyInstances) == 0 {
		return
	}

	c.logger.Info("draining specialists", "busy", len(busyInstances), "grace_period", gracePeriod)

	for _, inst := range busyInstances {
		c.sendShutdownNotification(ctx, inst)
	}

	if gracePeriod > 0 {
		select {
		case <-time.After(gracePeriod):
		case <-ctx.Done():
		}
	}
}

// forceCleanup kills all remaining specialist panes, cleans up maps, and sends
// a "coordinator exited" error to any instance with a pending result channel.
func (c *Core) forceCleanup(_ context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for roleName, inst := range c.specialists {
		c.logger.Info("killing specialist", "role", roleName)
		if p := inst.result.Load(); p != nil {
			select {
			case *p <- taskResult{err: "coordinator exited"}:
			default:
				// The Ask goroutine already consumed the result (or will shortly).
			}
		}
		if err := c.backend.Kill(context.Background(), inst.handle); err != nil {
			c.logger.Error("kill specialist failed", "role", roleName, "err", err)
		}
	}
	c.specialists = make(map[string]*instance)
	c.agentByID = make(map[string]types.Agent)
	c.instanceByAgentID = make(map[string]*instance)
}

// sendShutdownNotification sends a system notification to the specialist's
// terminal pane informing them that the coordinator has exited.
func (c *Core) sendShutdownNotification(ctx context.Context, inst *instance) {
	msg := "[SYSTEM] The coordinator has exited. The session will be terminated shortly."
	if err := c.backend.Send(ctx, inst.handle, msg); err != nil {
		c.logger.Error("send shutdown notification failed", "role", inst.role, "err", err)
	}
}
