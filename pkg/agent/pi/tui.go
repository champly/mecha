package pi

import agentprovider "github.com/champly/mecha/pkg/agent/provider"

var tuiProfile = agentprovider.TUIProfile{
	PasteMode: agentprovider.PasteLiteral,
	SubmitKey: []byte("\r"),
	Classify: agentprovider.CommonTUI{
		PromptAnchors: []string{"❯", "›", ">"},
		Placeholders:  []string{"type your message", "ask anything", "? for shortcuts"},
		BusyMarkers: []string{
			"esc to interrupt",
		},
		BlockedMarkers: []string{
			"do you trust the files in this folder",
			"trust folder only",
			"sign in to continue",
			"login required to continue",
			"approval required to continue",
		},
	}.Classify,
}

var _ agentprovider.TUIAgent = (*Pi)(nil)

// TUI returns the Pi TUI interaction profile.
func (p *Pi) TUI() agentprovider.TUIProfile { return tuiProfile }
