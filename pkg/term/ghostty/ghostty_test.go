package ghostty

import (
	"context"
	"strings"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestKillEmptyTerminalID(t *testing.T) {
	g := &Ghostty{}
	if err := g.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty terminal id: expected error, got nil")
	}
}

func TestSpawnResolvesAnchorOnce(t *testing.T) {
	var listCalls int
	var scripts []string
	run := func(_ context.Context, script string) (string, error) {
		scripts = append(scripts, script)
		if script == listTerminalsScript() {
			listCalls++
			return "ID-ANCHOR|/dev/ttys001\nID-OTHER|/dev/ttys002\n", nil
		}
		switch {
		case strings.Contains(script, "direction right"):
			return "ID-CHILD-1", nil
		case strings.Contains(script, "direction down"):
			return "ID-CHILD-2", nil
		default:
			t.Fatalf("unexpected script: %q", script)
			return "", nil
		}
	}

	g := &Ghostty{tty: "/dev/ttys001", run: run}
	ctx := context.Background()

	h1, err := g.Spawn(ctx, driver.Spec{Command: []string{"true"}})
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	if got := h1.PaneID(); got != "ID-CHILD-1" {
		t.Errorf("first Spawn pane = %q, want %q", got, "ID-CHILD-1")
	}
	if got := g.anchorID; got != "ID-ANCHOR" {
		t.Errorf("anchorID = %q, want %q", got, "ID-ANCHOR")
	}

	h2, err := g.Spawn(ctx, driver.Spec{Command: []string{"true"}})
	if err != nil {
		t.Fatalf("second Spawn: %v", err)
	}
	if got := h2.PaneID(); got != "ID-CHILD-2" {
		t.Errorf("second Spawn pane = %q, want %q", got, "ID-CHILD-2")
	}

	if listCalls != 1 {
		t.Errorf("terminal list ran %d times, want 1", listCalls)
	}
	if len(scripts) != 3 {
		t.Fatalf("ran %d scripts, want 3", len(scripts))
	}
	if !strings.Contains(scripts[1], quoteAppleScript("ID-ANCHOR")) {
		t.Errorf("first split did not target matched anchor: %q", scripts[1])
	}
	if !strings.Contains(scripts[2], quoteAppleScript("ID-CHILD-1")) {
		t.Errorf("second split did not target last child: %q", scripts[2])
	}
}

func TestSpawnFailsWithoutUniqueAnchor(t *testing.T) {
	tests := []struct {
		name string
		list string
	}{
		{"zero matches", "ID-1|/dev/ttys002\nID-2|/dev/ttys003\n"},
		{"duplicate matches", "ID-1|/dev/ttys001\nID-2|/dev/ttys001\n"},
		{"malformed output", "not-a-terminal-line\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			splitCalled := false
			run := func(_ context.Context, script string) (string, error) {
				if script != listTerminalsScript() {
					splitCalled = true
					return "ID-CHILD", nil
				}
				return tt.list, nil
			}

			g := &Ghostty{tty: "/dev/ttys001", run: run}
			if _, err := g.Spawn(context.Background(), driver.Spec{Command: []string{"true"}}); err == nil {
				t.Fatal("Spawn: expected error, got nil")
			}
			if splitCalled {
				t.Error("split script ran despite anchor failure")
			}
			if g.anchorID != "" {
				t.Errorf("anchorID cached on failure: %q", g.anchorID)
			}
		})
	}
}
