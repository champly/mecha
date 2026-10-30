package iterm2

import (
	"context"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

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
