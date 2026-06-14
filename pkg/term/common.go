package term

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
)

var handleSeq uint64

type paneHandle struct {
	id     string
	paneID string
}

// paneChain tracks spawned panes in right-side split order.
// Providers use it to pick the next split target and recover state after kill.
type paneChain struct {
	ids []string
}

func (h paneHandle) ID() string {
	return h.id
}

func (h paneHandle) PaneID() string {
	return h.paneID
}

func newHandle(prefix, paneID string) PaneHandle {
	n := atomic.AddUint64(&handleSeq, 1)
	return paneHandle{
		id:     fmt.Sprintf("%s-%d", prefix, n),
		paneID: paneID,
	}
}

func (c *paneChain) Empty() bool {
	return len(c.ids) == 0
}

func (c *paneChain) Len() int {
	return len(c.ids)
}

func (c *paneChain) Last() string {
	if len(c.ids) == 0 {
		return ""
	}
	return c.ids[len(c.ids)-1]
}

func (c *paneChain) Push(id string) {
	if id == "" {
		return
	}
	c.ids = append(c.ids, id)
}

func (c *paneChain) Remove(id string) {
	c.ids = removeString(c.ids, id)
}

func (c *paneChain) Reset() {
	c.ids = c.ids[:0]
}

func buildCommandLine(spec PaneSpec) string {
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
			parts = append(parts, shellQuote(k+"="+spec.Env[k]))
		}
	}

	for _, arg := range spec.Command {
		parts = append(parts, shellQuote(arg))
	}

	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// buildShellBootstrap builds a shell command that optionally cds to WorkDir
// before running the spec's command.
func buildShellBootstrap(spec PaneSpec) string {
	cmd := buildCommandLine(spec)
	if cmd == "" {
		return ""
	}
	if spec.WorkDir == "" {
		return cmd
	}
	return "cd " + shellQuote(spec.WorkDir) + " && exec " + cmd
}

// runAppleScript executes an osascript one-liner and returns trimmed stdout.
func runAppleScript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("term/applescript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// wrapAppScript wraps body inside a tell-block targeting the named macOS app.
func wrapAppScript(app, body string) string {
	return fmt.Sprintf(`tell application %q
%s
end tell`, app, body)
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

// removeString returns a new slice with the first occurrence of target removed.
func removeString(values []string, target string) []string {
	for i, v := range values {
		if v == target {
			out := make([]string, 0, len(values)-1)
			out = append(out, values[:i]...)
			return append(out, values[i+1:]...)
		}
	}
	return values
}

// applyMultilineInput iterates over text split by newline, calling sendText for
// non-empty parts and sendEnter between lines.
func applyMultilineInput(text string, sendText func(string) error, sendEnter func() error) error {
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		if part != "" {
			if err := sendText(part); err != nil {
				return err
			}
		}
		if i < len(parts)-1 {
			if err := sendEnter(); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildMultilineCommands turns multiline input into command snippets for script
// generation, preserving enter presses between lines.
func buildMultilineCommands(text string, buildText func(string) string, enter string) []string {
	parts := strings.Split(text, "\n")
	commands := make([]string, 0, len(parts)*2)
	for i, part := range parts {
		if part != "" {
			commands = append(commands, buildText(part))
		}
		if i < len(parts)-1 {
			commands = append(commands, enter)
		}
	}
	return commands
}
