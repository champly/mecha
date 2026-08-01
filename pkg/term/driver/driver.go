// Package driver defines the terminal pane backend contract and shared helpers.
package driver

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// Backend is the contract that all terminal providers implement.
type Backend interface {
	Spawn(ctx context.Context, spec Spec) (Handle, error)
	Kill(ctx context.Context, handle Handle) error
	// Close releases resources held by the backend (e.g. the iTerm2
	// WebSocket connection). Backends without held resources return nil.
	Close() error
	// Label labels the pane this process runs in with text (e.g. a role name).
	Label(text string) error
}

// Spec describes how to create a new terminal pane.
type Spec struct {
	WorkDir string
	Command []string
	Role    string
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

// BuildCommand builds a shell command line from spec. When spec.WorkDir is
// set, the command is prefixed with `cd <dir> &&` so backends that only send
// text into a shell (iTerm2, Ghostty) honor it; tmux also sets the pane cwd
// directly via split-window -c.
func BuildCommand(spec Spec) string {
	if len(spec.Command) == 0 {
		return ""
	}
	parts := make([]string, 0, len(spec.Command))
	for _, arg := range spec.Command {
		parts = append(parts, agenttypes.QuoteShell(arg))
	}
	cmd := strings.Join(parts, " ")
	if spec.WorkDir != "" {
		cmd = "cd " + agenttypes.QuoteShell(spec.WorkDir) + " && " + cmd
	}
	return cmd
}
