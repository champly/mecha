package codex

import (
	"strings"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

var codexTUI = agentprovider.CommonTUI{
	PromptAnchors: []string{"›", "❯", ">"},
	Placeholder:   "Write a message...",
	Placeholders:  []string{"ask codex to do anything", "type your message", "ask anything", "? for shortcuts"},
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
}

var tuiProfile = agentprovider.TUIProfile{
	PasteMode: agentprovider.PasteBracketed,
	SubmitKey: []byte("\r"),
	Classify: func(s agentprovider.ScreenSnapshot) agentprovider.Classification {
		switch strings.ToLower(s.AppState) {
		case "working", "running", "thinking":
			return agentprovider.Classification{State: agentprovider.InputBusy, Reason: "provider state " + s.AppState}
		case "blocked", "waiting", "approval":
			return agentprovider.Classification{State: agentprovider.InputBlocked, Reason: "provider state " + s.AppState}
		}
		if classified := codexTUI.Classify(s); classified.State != agentprovider.InputUnknown {
			return classified
		}
		if strings.EqualFold(s.AppState, "idle") {
			return agentprovider.Classification{State: agentprovider.InputEmpty}
		}
		return agentprovider.Classification{State: agentprovider.InputUnknown, Reason: "input prompt not recognized"}
	},
}

var _ agentprovider.TUIAgent = (*Codex)(nil)

// TUI returns the Codex TUI interaction profile.
func (c *Codex) TUI() agentprovider.TUIProfile { return tuiProfile }
