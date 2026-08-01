// Package ghostty provides a driver.Backend backed by Ghostty via AppleScript.
package ghostty

import (
	"context"
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
}

// New creates a new Ghostty provider. It pins the anchor to the terminal
// this process runs in — captured now, at process start — so spawned panes
// stay in the coordinator's window even when the user has focused another
// window or tab before the first Spawn. When the anchor cannot be resolved,
// Spawn falls back to the front window at spawn time.
func New() (driver.Backend, error) {
	g := &Ghostty{}
	ctx, cancel := context.WithTimeout(context.Background(), anchorTimeout)
	defer cancel()
	if out, err := runAppleScript(ctx, anchorScript()); err == nil {
		g.anchorTerm = strings.TrimSpace(out)
	}
	return g, nil
}

// Match reports whether the current environment is Ghostty.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty")
}

func (g *Ghostty) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
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
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := runAppleScript(ctx, closeScript(h.PaneID()))
	// Remove the id even when the close failed: a terminal whose process
	// already exited must not linger in the chain and break later spawns.
	g.terminals.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; Ghostty holds no resources.
func (g *Ghostty) Close() error {
	return nil
}
