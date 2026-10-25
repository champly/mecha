package ghostty

import (
	"context"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestKillEmptyTerminalID(t *testing.T) {
	g := &Ghostty{}
	if err := g.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty terminal id: expected error, got nil")
	}
}
