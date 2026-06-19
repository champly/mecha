// Package driver defines the terminal pane backend contract and shared helpers.
package driver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// Backend is the contract that all terminal providers implement.
type Backend interface {
	Spawn(ctx context.Context, spec Spec) (Handle, error)
	Send(ctx context.Context, handle Handle, text string) error
	Capture(ctx context.Context, handle Handle) (string, error)
	CaptureAll(ctx context.Context, handle Handle) (string, error)
	Kill(ctx context.Context, handle Handle) error
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

var idSeq atomic.Uint64

type ident struct {
	prefix   string
	nativeID string
}

func (h ident) ID() string {
	return h.prefix
}

func (h ident) PaneID() string {
	return h.nativeID
}

// NewID creates a new Handle with the given prefix and backend-native pane ID.
func NewID(prefix, nativeID string) Handle {
	n := idSeq.Add(1)
	return ident{prefix: fmt.Sprintf("%s-%d", prefix, n), nativeID: nativeID}
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

// BuildBootstrap returns a shell command that cds to WorkDir before running spec's command.
func BuildBootstrap(spec Spec) string {
	cmd := BuildCommand(spec)
	if cmd == "" {
		return ""
	}
	if spec.WorkDir == "" {
		return cmd
	}
	return "cd " + QuoteShell(spec.WorkDir) + " && exec " + cmd
}

// QuoteShell quotes s for safe use in a shell command.
func QuoteShell(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// ScriptMultiline splits multiline text into script commands with enter commands between lines.
func ScriptMultiline(text string, buildCmd func(string) string, enterCmd string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)*2)
	for i, line := range lines {
		if line != "" {
			out = append(out, buildCmd(line))
		}
		if i < len(lines)-1 {
			out = append(out, enterCmd)
		}
	}
	return out
}
