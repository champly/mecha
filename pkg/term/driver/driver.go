// Package driver defines the terminal pane backend contract and shared helpers.
package driver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

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
	Env     map[string]string
}

// Handle identifies a running terminal pane.
type Handle interface {
	ID() string
	PaneID() string
}

var (
	idSeq          atomic.Uint64
	shellSafeToken = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)
)

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

func (c *Chain) Len() int {
	return len(c.ids)
}

func (c *Chain) Reset() {
	c.ids = c.ids[:0]
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

// BuildCommand builds a shell command line from spec.
func BuildCommand(spec Spec) string {
	if len(spec.Command) == 0 {
		return ""
	}
	parts := make([]string, 0, len(spec.Env)+len(spec.Command)+1)
	if len(spec.Env) > 0 {
		parts = append(parts, "env")
		keys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, QuoteShell(k+"="+spec.Env[k]))
		}
	}
	for _, arg := range spec.Command {
		parts = append(parts, QuoteShell(arg))
	}
	return strings.Join(parts, " ")
}

// QuoteShell quotes s for safe use in a shell command. Shell-safe tokens are
// returned as-is; anything else is wrapped in single quotes — the only
// quoting that disables every shell expansion ($, backticks, globs, ...).
func QuoteShell(s string) string {
	if s == "" {
		return `''`
	}
	if shellSafeToken.MatchString(s) {
		return s
	}
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
