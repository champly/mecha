package term_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/term"
)

func TestNew(t *testing.T) {
	backend, err := term.New()
	if err == term.ErrUnsupported {
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
				Command: []string{"sleep", "30"},
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
		time.Sleep(3 * time.Second)
		for i, h := range handles {
			if err := backend.Kill(ctx, h); err != nil {
				t.Fatalf("kill %d: %v", i+1, err)
			}
			t.Logf("pane %d killed: %s", i+1, h.ID())
			time.Sleep(50 * time.Millisecond)
		}
		if err := waitPaneCount(ctx, initial); err != nil {
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := paneCount(ctx)
		if err != nil {
			return err
		}
		if n >= want {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	n, err := paneCount(ctx)
	if err != nil {
		return err
	}
	return fmt.Errorf("expected at least %d panes, got %d", want, n)
}
