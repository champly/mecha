package agent

import (
	"testing"

	"github.com/champly/mecha/pkg/config"
)

func TestNewFromConfigUnknownType(t *testing.T) {
	_, err := NewFromConfig("/ws", "prompt", "lead", "127.0.0.1:1",
		config.AgentConfig{Type: "nope"}, "mecha")
	if err == nil {
		t.Fatal("expected error for unknown agent type, got nil")
	}
}
