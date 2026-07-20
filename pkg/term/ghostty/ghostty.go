// Package ghostty provides a driver.Backend backed by Ghostty via AppleScript.
package ghostty

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/champly/mecha/pkg/term/driver"
)

const (
	prefix = "ghostty"
	app    = "Ghostty"
)

// Ghostty is a driver.Backend backed by Ghostty via AppleScript.
type Ghostty struct {
	mu        sync.Mutex
	windowID  string
	terminals driver.Chain
}

// New creates a new Ghostty provider.
func New() (driver.Backend, error) {
	return &Ghostty{}, nil
}

// Match reports whether the current environment is Ghostty.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty")
}

func (g *Ghostty) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	cmd := driver.BuildCommand(spec)

	if g.windowID == "" || g.terminals.Empty() {
		out, err := runAppleScript(ctx, firstSpawnScript(cmd))
		if err != nil {
			return nil, err
		}
		winID, termID, err := parseSpawnResult(out)
		if err != nil {
			return nil, err
		}
		g.windowID = winID
		g.terminals.Push(termID)
		return driver.NewHandle(prefix, termID), nil
	}

	out, err := runAppleScript(ctx, splitSpawnScript(g.windowID, g.terminals.Last(), cmd))
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
	if _, err := runAppleScript(ctx, closeScript(h.PaneID())); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.terminals.Remove(h.PaneID())
	if g.terminals.Empty() {
		g.windowID = ""
	}
	return nil
}
