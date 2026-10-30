package claude

import agentprovider "github.com/champly/mecha/pkg/agent/provider"

var tuiProfile = agentprovider.TUIProfile{
	PasteMode: agentprovider.PasteBracketed,
	SubmitKey: []byte("\r"),
	Classify: agentprovider.CommonTUI{
		PromptAnchors: []string{"❯", "›", ">"},
		Placeholders:  []string{"type your message", "ask anything", "? for shortcuts", "try \""},
		BusyMarkers: []string{
			"esc to interrupt",
		},
		BlockedMarkers: []string{
			"do you trust the files in this folder",
			"trust folder only",
			"sign in to continue",
			"login required to continue",
			"approval required to continue",
			"upgrade required to continue",
		},
	}.Classify,
}

var _ agentprovider.TUIAgent = (*Claude)(nil)

// TUI returns the Claude Code TUI interaction profile.
func (c *Claude) TUI() agentprovider.TUIProfile { return tuiProfile }
