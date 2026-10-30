// Package driver defines the terminal pane backend contract and shared helpers.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/champly/mecha/pkg/shellquote"
)

// ErrAnchorUnavailable reports that a matched backend cannot determine the
// terminal anchor (pane/session/terminal) for this process. Auto-detection
// treats it as "try the next backend"; an explicit Kind returns it as-is.
var ErrAnchorUnavailable = errors.New("term: anchor unavailable")

// Backend is the contract that all terminal providers implement.
type Backend interface {
	Spawn(ctx context.Context, spec Spec) (Handle, error)
	Kill(ctx context.Context, handle Handle) error
	// Close releases resources held by the backend (e.g. the iTerm2
	// WebSocket connection). Backends without held resources return nil.
	Close() error
}

// Spec describes how to create a new terminal pane.
type Spec struct {
	WorkDir string
	Command []string
	Role    string
	// Env is appended to the backend's base environment and overrides any
	// duplicate keys. Nil means inherit the base environment unchanged.
	Env []string
	// Stdout receives PTY output (or the pane equivalent) for this spawn.
	// Nil means use the backend default; backends without a default discard it.
	Stdout io.Writer
	// Stderr receives the child's stderr. Nil means use the backend default;
	// headless merges it into the PTY when no sink is configured.
	Stderr io.Writer
}

// Handle identifies a running terminal pane.
type Handle interface {
	ID() string
	PaneID() string
}

var idSeq atomic.Uint64

type ident struct {
	displayID string
	nativeID  string
}

func (h ident) ID() string {
	return h.displayID
}

func (h ident) PaneID() string {
	return h.nativeID
}

// NewHandle creates a new Handle with the given prefix and backend-native pane ID.
func NewHandle(prefix, nativeID string) Handle {
	n := idSeq.Add(1)
	return ident{displayID: fmt.Sprintf("%s-%d", prefix, n), nativeID: nativeID}
}

// Chain tracks spawned panes in right-side split order.
type Chain struct {
	ids []string
}

func (c *Chain) Empty() bool {
	return len(c.ids) == 0
}

func (c *Chain) Last() string {
	if len(c.ids) == 0 {
		return ""
	}
	return c.ids[len(c.ids)-1]
}

func (c *Chain) Push(id string) {
	if id != "" {
		c.ids = append(c.ids, id)
	}
}

func (c *Chain) Remove(id string) {
	for i, v := range c.ids {
		if v == id {
			c.ids = append(c.ids[:i], c.ids[i+1:]...)
			return
		}
	}
}

// BuildCommand builds a shell command line from spec. The command owns its
// working directory (agentd takes --dir); WorkDir still reaches backends that
// can apply it natively.
func BuildCommand(spec Spec) string {
	if len(spec.Command) == 0 {
		return ""
	}
	parts := make([]string, 0, len(spec.Command))
	for _, arg := range spec.Command {
		parts = append(parts, shellquote.Quote(arg))
	}
	return strings.Join(parts, " ")
}
