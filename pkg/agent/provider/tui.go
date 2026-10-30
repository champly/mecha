package provider

import "strings"

// InputState describes what the provider's TUI input area currently allows.
type InputState uint8

const (
	InputUnknown InputState = iota
	InputEmpty
	InputDraft
	InputBusy
	InputBlocked
)

func (s InputState) String() string {
	switch s {
	case InputEmpty:
		return "empty"
	case InputDraft:
		return "draft"
	case InputBusy:
		return "busy"
	case InputBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// PasteMode describes how task text is written into the provider's TUI.
type PasteMode uint8

const (
	PasteLiteral PasteMode = iota
	PasteBracketed
)

// ScreenSnapshot is an immutable view of the visible terminal at one version.
type ScreenSnapshot struct {
	Version        uint64
	Rows           []string
	CursorX        int
	CursorY        int
	Alternate      bool
	BracketedPaste bool
	AppID          string
	AppState       string
	ContentHash    uint64
}

// Classification is the provider-specific interpretation of a screen.
type Classification struct {
	State  InputState
	Reason string
}

// TUIProfile declares the PTY interaction capabilities of one agent.
type TUIProfile struct {
	PasteMode PasteMode
	SubmitKey []byte
	// Classify returns InputUnknown when the visible screen is not recognized.
	Classify func(ScreenSnapshot) Classification
}

// TUIAgent is implemented by agents that can be driven through their TUI.
type TUIAgent interface {
	TUI() TUIProfile
}

// CommonTUI supplies conservative classifications shared by the supported
// agent CLIs. Provider profiles add their own anchors and blocked markers.
type CommonTUI struct {
	PromptAnchors   []string
	Placeholder     string
	Placeholders    []string
	BusyMarkers     []string
	BlockedMarkers  []string
	NoInputMarkers  []string
	ResetPromptHint bool
}

// Classify applies the fixed priority: blocked, busy, draft, empty, unknown.
// It only inspects the bottom portion of the screen, where all supported TUIs
// render their editor and status line.
func (c CommonTUI) Classify(s ScreenSnapshot) Classification {
	rows := bottomRows(s.Rows, 14)
	text := strings.Join(rows, "\n")
	lower := strings.ToLower(text)

	for _, marker := range c.BlockedMarkers {
		if marker != "" && strings.Contains(lower, strings.ToLower(marker)) {
			return Classification{State: InputBlocked, Reason: marker}
		}
	}
	for _, marker := range c.BusyMarkers {
		if marker != "" && strings.Contains(lower, strings.ToLower(marker)) {
			return Classification{State: InputBusy, Reason: marker}
		}
	}
	for _, marker := range c.NoInputMarkers {
		if marker != "" && strings.Contains(lower, strings.ToLower(marker)) {
			return Classification{State: InputUnknown, Reason: marker}
		}
	}

	for i := len(rows) - 1; i >= 0; i-- {
		value, ok := InputLineValue(rows[i], c.PromptAnchors)
		if !ok {
			continue
		}
		if c.isPlaceholder(value) {
			return Classification{State: InputEmpty}
		}
		return Classification{State: InputDraft}
	}

	return Classification{State: InputUnknown, Reason: "input prompt not recognized"}
}

func (c CommonTUI) isPlaceholder(value string) bool {
	if value == "" || (c.Placeholder != "" && strings.EqualFold(value, c.Placeholder)) {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range c.Placeholders {
		if marker != "" && strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func InputLineValue(row string, anchors []string) (string, bool) {
	row = strings.TrimLeft(row, " \t│┃║|")
	for _, anchor := range anchors {
		if strings.HasPrefix(row, anchor) {
			return strings.TrimSpace(row[len(anchor):]), true
		}
	}
	return "", false
}

func bottomRows(rows []string, n int) []string {
	if len(rows) <= n {
		return rows
	}
	return rows[len(rows)-n:]
}

// NormalizePrompt makes provider hook prompts and locally sent tasks comparable.
func NormalizePrompt(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}
