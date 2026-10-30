// Package ghostty provides a driver.Backend backed by Ghostty via AppleScript.
package ghostty

import (
	"context"
	"errors"
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
	tty       string
	anchorID  string
	terminals driver.Chain
	run       func(context.Context, string) (string, error)
}

// New creates a new Ghostty provider. It synchronously captures the
// controlling tty of this process; the terminal id anchor is resolved
// lazily on the first Spawn by matching that tty. New never runs AppleScript.
func New() (driver.Backend, error) {
	tty, err := ownTTY()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", driver.ErrAnchorUnavailable, err)
	}
	return &Ghostty{tty: tty, run: runAppleScript}, nil
}

// Match reports whether the current environment is Ghostty.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty")
}

func (g *Ghostty) runner() func(context.Context, string) (string, error) {
	if g.run != nil {
		return g.run
	}
	return runAppleScript
}

func (g *Ghostty) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	run := g.runner()
	cmd := driver.BuildCommand(spec)

	var script string
	if g.terminals.Empty() {
		if g.anchorID == "" {
			anchorID, err := resolveAnchorID(ctx, run, g.tty)
			if err != nil {
				return nil, err
			}
			g.anchorID = anchorID
		}
		script = anchorSpawnScript(g.anchorID, cmd)
	} else {
		script = splitSpawnScript(g.terminals.Last(), cmd)
	}

	out, err := run(ctx, script)
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

// resolveAnchorID maps the controlling tty to the unique Ghostty terminal id.
// It never falls back to focus-based lookup.
func resolveAnchorID(ctx context.Context, run func(context.Context, string) (string, error), tty string) (string, error) {
	out, err := run(ctx, listTerminalsScript())
	if err != nil {
		return "", err
	}
	return matchAnchor(out, tty)
}

// matchAnchor parses "id|tty" lines and requires exactly one tty match.
func matchAnchor(out, tty string) (string, error) {
	tty = canonicalTTY(tty)
	if tty == "" {
		return "", errors.New("term/ghostty: empty controlling tty")
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("term/ghostty: terminal list is empty")
	}

	var matches []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, rawTTY, ok := strings.Cut(line, "|")
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return "", fmt.Errorf("term/ghostty: malformed terminal list line %q", line)
		}
		if canonicalTTY(rawTTY) == tty {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("term/ghostty: no terminal matches controlling tty %s", tty)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("term/ghostty: %d terminals match controlling tty %s", len(matches), tty)
	}
}

func (g *Ghostty) Kill(ctx context.Context, h driver.Handle) error {
	if h == nil || h.PaneID() == "" {
		return errors.New("term/ghostty: empty terminal id")
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.runner()(ctx, closeScript(h.PaneID()))
	// Drop the id even on failure, so a dead terminal cannot break later spawns.
	g.terminals.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; Ghostty holds no resources.
func (g *Ghostty) Close() error {
	return nil
}
