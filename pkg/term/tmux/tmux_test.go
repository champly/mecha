package tmux

import (
	"context"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestKillEmptyPaneID(t *testing.T) {
	tm := &Tmux{}
	if err := tm.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty pane id: expected error, got nil")
	}
}
