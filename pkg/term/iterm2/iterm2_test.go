package iterm2

import (
	"bytes"
	"context"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestBadgeCommand(t *testing.T) {
	const want = `printf '\033]1337;SetBadgeFormat=Y29kZXI=\007'`
	if got := badgeCommand("coder"); got != want {
		t.Errorf("badgeCommand() = %q, want %q", got, want)
	}
}

func TestLabelWritesBadge(t *testing.T) {
	var buf bytes.Buffer
	p := &ITerm2{out: &buf}

	if err := p.Label("coder"); err != nil {
		t.Fatalf("Label: %v", err)
	}

	want := "\x1b]1337;SetBadgeFormat=" + badgePayload("coder") + "\x07"
	if got := buf.String(); got != want {
		t.Errorf("Label wrote %q, want %q", got, want)
	}
}

func TestKillEmptyPaneID(t *testing.T) {
	p := &ITerm2{}
	if err := p.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty pane id: expected error, got nil")
	}
}

func TestNewFailsWithoutSession(t *testing.T) {
	t.Setenv("ITERM_SESSION_ID", "")
	if _, err := New(); err == nil {
		t.Fatal("New with empty ITERM_SESSION_ID: expected error, got nil")
	}
}
