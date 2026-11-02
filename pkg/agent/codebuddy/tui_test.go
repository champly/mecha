package codebuddy

import (
	"testing"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

func TestTUIClassifyBootScreen(t *testing.T) {
	rows := []string{
		"╭─── CodeBuddy Code v2.179.2 ───╮",
		"╰───────────────────────────────╯",
		"",
		"────────────────────────────────",
		"> 让  architec 看 看",
		"────────────────────────────────",
		"⏵⏵ bypass permissions on (shift+Tab to cycle)  ← for agents",
	}
	for range 40 {
		rows = append(rows, "")
	}
	got := tuiProfile.Classify(agentprovider.ScreenSnapshot{Rows: rows})
	if got.State != agentprovider.InputDraft {
		t.Fatalf("state = %v (%s), want draft", got.State, got.Reason)
	}
	rows[4] = ">"
	got = tuiProfile.Classify(agentprovider.ScreenSnapshot{Rows: rows})
	if got.State != agentprovider.InputEmpty {
		t.Fatalf("state = %v (%s), want empty", got.State, got.Reason)
	}
}
