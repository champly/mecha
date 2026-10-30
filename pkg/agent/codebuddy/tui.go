package codebuddy

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

var _ agentprovider.TUIAgent = (*CodeBuddy)(nil)

// TUI returns the CodeBuddy TUI interaction profile.
func (c *CodeBuddy) TUI() agentprovider.TUIProfile { return tuiProfile }
