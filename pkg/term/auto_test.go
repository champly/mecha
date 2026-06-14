package term

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestNewAutoProvider tests that the correct provider is selected based on environment
// and can successfully create and destroy terminal panes.
// Run this test in different terminal environments:
// - tmux: TMUX variable set
// - iTerm2: TERM_PROGRAM=iTerm.app or iTerm2.app
// - Ghostty: TERM_PROGRAM=ghostty
func TestNewAutoProvider(t *testing.T) {
	p, err := NewAutoProvider()
	if err == ErrUnsupportedEnvironment {
		t.Skip("unsupported terminal environment for this test")
	}
	if err != nil {
		t.Fatalf("NewAutoProvider failed: %v", err)
	}
	if p == nil {
		t.Fatal("expected a provider, got nil")
	}

	ctx := context.Background()
	handles := make([]PaneHandle, 0, 3)
	initialCount, err := visibleSessionCount(ctx)
	if err != nil {
		t.Fatalf("failed to read initial session count: %v", err)
	}

	t.Run("spawn layout", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			h, spawnErr := spawnAndAssert(t, ctx, p, specialistPaneSpec(), i+1, initialCount+i+1)
			if spawnErr != nil {
				t.Fatalf("spawn %d failed: %v", i+1, spawnErr)
			}
			handles = append(handles, h)
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("kill recover", func(t *testing.T) {
		time.Sleep(3 * time.Second)
		for i, h := range handles {
			if err := p.Kill(ctx, h); err != nil {
				t.Fatalf("Kill pane %d failed: %v", i+1, err)
			}
			t.Logf("Pane %d killed: %s", i+1, h.ID())
			time.Sleep(50 * time.Millisecond)
		}

		if err := waitForVisibleSessionCount(ctx, initialCount); err != nil {
			t.Fatalf("pane count after kill: %v", err)
		}
	})
}

func specialistPaneSpec() PaneSpec {
	return PaneSpec{
		WorkDir: "/tmp",
		Command: []string{"sleep", "30"},
		Env:     nil,
	}
}

func spawnAndAssert(t *testing.T, ctx context.Context, p PaneBackend, spec PaneSpec, index, expectedCount int) (PaneHandle, error) {
	t.Helper()

	h, err := p.Spawn(ctx, spec)
	if err != nil {
		return nil, err
	}
	if h == nil {
		t.Fatal("expected a handle, got nil")
	}
	t.Logf("Pane %d created: %s (ID: %s)", index, h.ID(), h.PaneID())
	if err := waitForVisibleSessionCount(ctx, expectedCount); err != nil {
		return nil, fmt.Errorf("pane count after spawn %d: %w", index, err)
	}
	return h, nil
}

func visibleSessionCount(ctx context.Context) (int, error) {
	if os.Getenv("TMUX") != "" {
		return tmuxPaneCount(ctx)
	}
	var script string
	if strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "ghostty") {
		script = `tell application "Ghostty" to return count of terminals of selected tab of front window`
	} else {
		script = `tell application "iTerm2" to tell current window to return count of sessions of current tab`
	}
	out, err := runAppleScript(ctx, script)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, err
	}
	return count, nil
}

func tmuxPaneCount(ctx context.Context) (int, error) {
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-t", os.Getenv("TMUX_PANE"), "-s").CombinedOutput()
	if err != nil {
		// fall back to counting all panes in the current window
		out, err = exec.CommandContext(ctx, "tmux", "list-panes", "-s").CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("tmux list-panes: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	for _, l := range lines {
		if l != "" {
			count++
		}
	}
	return count, nil
}

func waitForVisibleSessionCount(ctx context.Context, expected int) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		count, err := visibleSessionCount(ctx)
		if err != nil {
			return err
		}
		if count >= expected {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	count, err := visibleSessionCount(ctx)
	if err != nil {
		return err
	}
	return fmt.Errorf("expected at least %d visible sessions, got %d", expected, count)
}
