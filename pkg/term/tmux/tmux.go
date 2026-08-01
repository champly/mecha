// Package tmux provides a driver.Backend backed by tmux.
package tmux

import (
	"context"
	"os"
	"sync"

	"github.com/champly/mecha/pkg/term/driver"
)

const (
	binary    = "tmux"
	splitSize = "50"
	paneFmt   = `#{pane_id}`
	errFmt    = `term/tmux: %s %s failed: %w: %s`
)

// Tmux is a driver.Backend backed by tmux.
type Tmux struct {
	mu         sync.Mutex
	anchorPane string
	panes      driver.Chain
}

// New creates a new Tmux provider. The anchor pane is pinned to the pane
// this process runs in (TMUX_PANE, i.e. the coordinator's pane), so spawned
// panes stay in the coordinator's window even when the user has switched to
// another window before the first Spawn. When TMUX_PANE is empty, Spawn
// falls back to resolving the current pane lazily.
func New() (driver.Backend, error) {
	return &Tmux{anchorPane: os.Getenv("TMUX_PANE")}, nil
}

// Match reports whether the current environment is tmux.
func Match() bool {
	return os.Getenv("TMUX") != "" && commandExists("tmux")
}

func (t *Tmux) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.anchorPane == "" {
		anchor, err := currentPane(ctx)
		if err != nil {
			return nil, err
		}
		t.anchorPane = anchor
	}

	var paneID string
	var err error
	if t.panes.Empty() {
		paneID, err = splitRight(ctx, t.anchorPane, spec.WorkDir)
	} else {
		paneID, err = splitDown(ctx, t.panes.Last(), spec.WorkDir)
	}
	if err != nil {
		return nil, err
	}

	if cmd := driver.BuildCommand(spec); cmd != "" {
		if err := sendLiteral(ctx, paneID, cmd); err != nil {
			// The split already happened; destroy the pane, keep it in the
			// chain only when the kill fails.
			if _, kerr := tmux(ctx, "kill-pane", "-t", paneID); kerr != nil {
				t.panes.Push(paneID)
			}
			return nil, err
		}
		if err := sendEnter(ctx, paneID); err != nil {
			if _, kerr := tmux(ctx, "kill-pane", "-t", paneID); kerr != nil {
				t.panes.Push(paneID)
			}
			return nil, err
		}
	}

	t.panes.Push(paneID)
	return driver.NewHandle("tmux", paneID), nil
}

func (t *Tmux) Kill(ctx context.Context, h driver.Handle) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := tmux(ctx, "kill-pane", "-t", h.PaneID())
	// Remove the id even when the kill failed: a pane whose process already
	// exited must not linger in the chain and break later spawns.
	t.panes.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; tmux holds no resources.
func (t *Tmux) Close() error {
	return nil
}

// Label implements driver.Backend; tmux has no badge.
func (t *Tmux) Label(text string) error {
	return nil
}
