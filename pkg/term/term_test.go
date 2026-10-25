package term_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/term"
)

// lockedBuffer is safe to read while a backend goroutine writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestNew(t *testing.T) {
	backend, err := term.New()
	if errors.Is(err, term.ErrUnsupported) {
		t.Skip("unsupported terminal environment")
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	initial, err := paneCount(ctx)
	if err != nil {
		t.Fatalf("initial pane count: %v", err)
	}

	handles := make([]term.Handle, 0, 3)

	t.Run("spawn", func(t *testing.T) {
		for i := range 3 {
			h, err := backend.Spawn(ctx, term.Spec{
				WorkDir: "/tmp",
				Command: []string{"sh", "-c", "date; exec sleep 30"},
			})
			if err != nil {
				t.Fatalf("spawn %d: %v", i+1, err)
			}
			handles = append(handles, h)
			time.Sleep(100 * time.Millisecond)
		}
		if err := waitPaneCount(ctx, initial+3); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("kill", func(t *testing.T) {
		time.Sleep(1 * time.Second)
		for i, h := range handles {
			if err := backend.Kill(ctx, h); err != nil {
				t.Fatalf("kill %d: %v", i+1, err)
			}
			t.Logf("pane %d killed: %s", i+1, h.ID())
			time.Sleep(50 * time.Millisecond)
		}
		if err := waitPaneCountAtMost(ctx, initial); err != nil {
			t.Fatal(err)
		}
	})
}

func paneCount(ctx context.Context) (int, error) {
	if os.Getenv("TMUX") != "" {
		out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-t", os.Getenv("TMUX_PANE"), "-s").CombinedOutput()
		if err != nil {
			out, err = exec.CommandContext(ctx, "tmux", "list-panes", "-s").CombinedOutput()
			if err != nil {
				return 0, fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		n := 0
		for _, l := range lines {
			if l != "" {
				n++
			}
		}
		return n, nil
	}
	var script string
	if strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty") {
		script = `tell application "Ghostty" to return count of terminals of selected tab of front window`
	} else {
		script = `tell application "iTerm2" to tell current window to return count of sessions of current tab`
	}
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("term_test: osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func waitPaneCount(ctx context.Context, want int) error {
	return waitPaneCountUntil(ctx, fmt.Sprintf("at least %d panes", want), func(n int) bool { return n >= want })
}

// waitPaneCountAtMost waits until the pane count drops to at most want
// (kill direction; a >= check would pass while panes were still alive).
func waitPaneCountAtMost(ctx context.Context, want int) error {
	return waitPaneCountUntil(ctx, fmt.Sprintf("at most %d panes", want), func(n int) bool { return n <= want })
}

func waitPaneCountUntil(ctx context.Context, desc string, ok func(int) bool) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := paneCount(ctx)
		if err != nil {
			return err
		}
		if ok(n) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	n, err := paneCount(ctx)
	if err != nil {
		return err
	}
	return fmt.Errorf("expected %s, got %d", desc, n)
}

func TestNewHeadlessSelection(t *testing.T) {
	backend, err := term.New(term.Options{Kind: "headless"})
	if err != nil {
		t.Fatalf("New(headless): %v", err)
	}
	defer backend.Close()
	h, err := backend.Spawn(context.Background(), term.Spec{Command: []string{"true"}})
	if err != nil {
		t.Fatalf("headless Spawn: %v", err)
	}
	if err := backend.Kill(context.Background(), h); err != nil {
		t.Fatalf("headless Kill: %v", err)
	}
}

func TestNewAutoUnchangedWithoutHeadless(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM_PROGRAM", "")
	if _, err := term.New(); !errors.Is(err, term.ErrUnsupported) {
		t.Fatalf("New() = %v, want ErrUnsupported", err)
	}

	if _, err := term.New(term.Options{Kind: "unknown"}); !errors.Is(err, term.ErrUnsupported) {
		t.Fatalf("New(unknown) = %v, want ErrUnsupported", err)
	}

	backend, err := term.New(term.Options{AllowHeadless: true})
	if err != nil {
		t.Fatalf("New(AllowHeadless): %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewHeadlessOutputWriters(t *testing.T) {
	var stdout, stderr lockedBuffer
	backend, err := term.New(term.Options{
		Kind:   "headless",
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("New(headless): %v", err)
	}
	defer backend.Close()

	h, err := backend.Spawn(context.Background(), term.Spec{
		Command: []string{"sh", "-c", `printf 'OUT-LINE\n'; printf 'ERR-LINE\n' >&2`},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "OUT-LINE") && strings.Contains(stderr.String(), "ERR-LINE") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := backend.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "OUT-LINE") {
		t.Errorf("stdout writer got %q, want OUT-LINE", got)
	}
	if got := stderr.String(); !strings.Contains(got, "ERR-LINE") {
		t.Errorf("stderr writer got %q, want ERR-LINE", got)
	}
}
