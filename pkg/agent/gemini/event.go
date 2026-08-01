package gemini

import (
	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventAfterAgent fires after each agent loop iteration carrying prompt_response;
// it maps to EventStop, analogous to Claude's Stop/last_assistant_message.
const eventAfterAgent = "AfterAgent"

// eventMap converts Gemini CLI's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart":  agenttypes.EventSessionStart,
	eventAfterAgent: agenttypes.EventStop,
}

// ParseHookEvent parses raw Gemini CLI Hook JSON into a unified HookEvent.
//
// Gemini CLI hook events share these common fields:
//
//	hook_event_name  string   — event name
//	session_id       string   — session identifier
//	transcript_path  string   — path to session transcript
//	cwd              string   — current working directory
//	timestamp        string   — ISO 8601 execution time
//
// Event-specific fields:
//
//	AfterAgent:     prompt_response  string  — assistant's response text
//	SessionStart:   source           string  — startup | resume | clear
func (g *Gemini) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	return agenttypes.ParseHook("gemini", raw, eventMap, func(m map[string]any, e *agenttypes.HookEvent) {
		switch e.Event {
		case agenttypes.EventStop:
			if msg, ok := m["prompt_response"].(string); ok && msg != "" {
				e.Output = msg
			}
		}
	})
}
