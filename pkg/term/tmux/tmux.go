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

// Tmux is a driver.Provider backed by tmux.
type Tmux struct {
	mu         sync.Mutex
	anchorPane string
	panes      driver.Chain
}

// New creates a new Tmux provider.
func New() (driver.Backend, error) {
	return &Tmux{}, nil
}

// Name returns the driver name.
func (t *Tmux) Name() string {
	return "tmux"
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

	if cmd := driver.BuildBootstrap(spec); cmd != "" {
		if err := sendLiteral(ctx, paneID, cmd); err != nil {
			return nil, err
		}
		if err := sendEnter(ctx, paneID); err != nil {
			return nil, err
		}
	}

	t.panes.Push(paneID)
	return driver.NewID("tmux", paneID), nil
}

func (t *Tmux) Send(ctx context.Context, h driver.Handle, text string) error {
	return sendMultiline(text,
		func(p string) error {
			return sendLiteral(ctx, h.PaneID(), p)
		},
		func() error {
			return sendEnter(ctx, h.PaneID())
		},
	)
}

func (t *Tmux) Capture(ctx context.Context, h driver.Handle) (string, error) {
	return tmux(ctx, captureArgs(false, h.PaneID())...)
}

func (t *Tmux) CaptureAll(ctx context.Context, h driver.Handle) (string, error) {
	return tmux(ctx, captureArgs(true, h.PaneID())...)
}

func (t *Tmux) Kill(ctx context.Context, h driver.Handle) error {
	if _, err := tmux(ctx, "kill-pane", "-t", h.PaneID()); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.panes.Remove(h.PaneID())
	return nil
}
