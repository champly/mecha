// Package ghostty provides a driver.Backend backed by Ghostty via AppleScript.
package ghostty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/champly/mecha/pkg/term/driver"
)

const (
	prefix        = "ghostty"
	app           = "Ghostty"
	anchorTimeout = 5 * time.Second
)

// Ghostty is a driver.Backend backed by Ghostty via AppleScript.
type Ghostty struct {
	mu         sync.Mutex
	anchorTerm string
	terminals  driver.Chain
	anchorDone chan struct{}
}

// New creates a new Ghostty provider. The anchor terminal is resolved in the
// background so startup does not block on AppleScript (which may wait for the
// macOS Automation permission prompt). Spawn waits for that resolution before
// splitting; when no anchor is found it falls back to the front window.
func New() (driver.Backend, error) {
	g := &Ghostty{anchorDone: make(chan struct{})}
	go g.resolveAnchor()
	return g, nil
}

// resolveAnchor pins the terminal this process started in.
func (g *Ghostty) resolveAnchor() {
	defer close(g.anchorDone)

	ctx, cancel := context.WithTimeout(context.Background(), anchorTimeout)
	defer cancel()
	out, err := runAppleScript(ctx, anchorScript())
	if err != nil {
		return
	}
	g.mu.Lock()
	g.anchorTerm = strings.TrimSpace(out)
	g.mu.Unlock()
}

// Match reports whether the current environment is Ghostty.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty")
}

func (g *Ghostty) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	if g.anchorDone != nil {
		select {
		case <-g.anchorDone:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	cmd := driver.BuildCommand(spec)

	if g.terminals.Empty() {
		if g.anchorTerm != "" {
			out, err := runAppleScript(ctx, anchorSpawnScript(g.anchorTerm, cmd))
			if err != nil {
				return nil, err
			}
			termID := strings.TrimSpace(out)
			if termID == "" {
				return nil, fmt.Errorf("term/ghostty: empty terminal id after split")
			}
			g.terminals.Push(termID)
			return driver.NewHandle(prefix, termID), nil
		}
		out, err := runAppleScript(ctx, firstSpawnScript(cmd))
		if err != nil {
			return nil, err
		}
		termID := strings.TrimSpace(out)
		if termID == "" {
			return nil, fmt.Errorf("term/ghostty: empty terminal id after split")
		}
		g.terminals.Push(termID)
		return driver.NewHandle(prefix, termID), nil
	}

	out, err := runAppleScript(ctx, splitSpawnScript(g.terminals.Last(), cmd))
	if err != nil {
		return nil, err
	}
	termID := strings.TrimSpace(out)
	if termID == "" {
		return nil, fmt.Errorf("term/ghostty: empty terminal id after split")
	}
	g.terminals.Push(termID)
	return driver.NewHandle(prefix, termID), nil
}

func (g *Ghostty) Kill(ctx context.Context, h driver.Handle) error {
	if h == nil || h.PaneID() == "" {
		return errors.New("term/ghostty: empty terminal id")
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := runAppleScript(ctx, closeScript(h.PaneID()))
	// Drop the id even on failure, so a dead terminal cannot break later spawns.
	g.terminals.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; Ghostty holds no resources.
func (g *Ghostty) Close() error {
	return nil
}

// Label implements driver.Backend; Ghostty has no badge.
func (g *Ghostty) Label(text string) error {
	return nil
}
