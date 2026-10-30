// Package tmux provides a driver.Backend backed by tmux.
package tmux

import (
	"context"
	"errors"
	"fmt"
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

// New creates a new Tmux provider, pinning the anchor to this process's pane
// (TMUX_PANE). New fails when TMUX_PANE is empty so panes are never created
// in another pane.
func New() (driver.Backend, error) {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		err := errors.New("term/tmux: TMUX_PANE is empty")
		return nil, fmt.Errorf("%w: %w", driver.ErrAnchorUnavailable, err)
	}
	return &Tmux{anchorPane: pane}, nil
}

// Match reports whether the current environment is tmux.
func Match() bool {
	return os.Getenv("TMUX") != "" && commandExists("tmux")
}

func (t *Tmux) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.anchorPane == "" {
		return nil, errors.New("term/tmux: empty anchor pane")
	}
	command := driver.BuildCommand(spec)

	var paneID string
	var err error
	if t.panes.Empty() {
		paneID, err = splitRight(ctx, t.anchorPane, spec.WorkDir, command)
	} else {
		paneID, err = splitDown(ctx, t.panes.Last(), spec.WorkDir, command)
	}
	if err != nil {
		return nil, err
	}

	t.panes.Push(paneID)
	return driver.NewHandle("tmux", paneID), nil
}

func (t *Tmux) Kill(ctx context.Context, h driver.Handle) error {
	if h == nil || h.PaneID() == "" {
		return errors.New("term/tmux: empty pane id")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := tmux(ctx, "kill-pane", "-t", h.PaneID())
	// Drop the id even on failure, so a dead pane cannot break later spawns.
	t.panes.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; tmux holds no resources.
func (t *Tmux) Close() error {
	return nil
}
