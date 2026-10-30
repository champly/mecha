package provider

import "testing"

func TestCommonTUIClassificationPriority(t *testing.T) {
	classify := CommonTUI{
		PromptAnchors:  []string{"›"},
		Placeholders:   []string{"type your message"},
		BusyMarkers:    []string{"esc to interrupt"},
		BlockedMarkers: []string{"approval required"},
	}.Classify

	tests := []struct {
		name  string
		rows  []string
		state InputState
	}{
		{"blocked wins over empty prompt", []string{"›", "approval required"}, InputBlocked},
		{"busy wins over empty prompt", []string{"›", "esc to interrupt"}, InputBusy},
		{"empty prompt", []string{"conversation", "›"}, InputEmpty},
		{"placeholder prompt", []string{"› Type your message..."}, InputEmpty},
		{"draft prompt", []string{"› user draft"}, InputDraft},
		{"unknown screen", []string{"loading"}, InputUnknown},
		{"anchor must start the line", []string{"text ›"}, InputUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(ScreenSnapshot{Rows: tt.rows})
			if got.State != tt.state {
				t.Errorf("state = %s, want %s (%+v)", got.State, tt.state, got)
			}
		})
	}
}

func TestNormalizePrompt(t *testing.T) {
	if got := NormalizePrompt("  a\r\nb\rc  "); got != "a\nb\nc" {
		t.Errorf("NormalizePrompt = %q", got)
	}
}
