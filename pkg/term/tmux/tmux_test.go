package tmux

import (
	"context"
	"slices"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestKillEmptyPaneID(t *testing.T) {
	tm := &Tmux{}
	if err := tm.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty pane id: expected error, got nil")
	}
}

func TestSplitArgsRunsCommandDirectly(t *testing.T) {
	got := splitArgs("-h", "%1", "/tmp/work dir", "mecha agentd --dir '/tmp/work dir'")
	want := []string{
		"split-window", "-h", "-p", splitSize, "-P", "-F", paneFmt,
		"-t", "%1", "-c", "/tmp/work dir", "mecha agentd --dir '/tmp/work dir'",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("splitArgs = %#v, want %#v", got, want)
	}
}

func TestNewFailsWithoutPane(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	if _, err := New(); err == nil {
		t.Fatal("New with empty TMUX_PANE: expected error, got nil")
	}
}
