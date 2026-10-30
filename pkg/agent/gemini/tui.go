package gemini

import agentprovider "github.com/champly/mecha/pkg/agent/provider"

var tuiProfile = agentprovider.TUIProfile{
	PasteMode: agentprovider.PasteLiteral,
	SubmitKey: []byte("\r"),
	Classify: agentprovider.CommonTUI{
		PromptAnchors: []string{">", "❯", "›"},
		Placeholders:  []string{"type your message", "ask anything", "? for shortcuts"},
		BusyMarkers: []string{
			"esc to cancel",
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

var _ agentprovider.TUIAgent = (*Gemini)(nil)

// TUI returns the Gemini CLI TUI interaction profile.
func (g *Gemini) TUI() agentprovider.TUIProfile { return tuiProfile }
