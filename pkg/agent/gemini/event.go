package gemini

import (
	"encoding/json"
	"fmt"

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
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return agenttypes.HookEvent{}, fmt.Errorf("gemini: parse hook event: %w", err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("gemini: hook_event_name missing or not a string")
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("gemini: unknown hook event %q", hookEventName)
	}

	e := agenttypes.HookEvent{
		Event: event,
		Raw:   raw,
	}

	if sid, ok := m["session_id"].(string); ok {
		e.SessionID = sid
	}

	switch event {
	case agenttypes.EventStop:
		if msg, ok := m["prompt_response"].(string); ok {
			e.Output = msg
		}
		e.OutputSource = "provider_field"
	}

	return e, nil
}
