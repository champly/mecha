package codex

import (
	"encoding/json"
	"fmt"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventMap converts Codex's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart": agenttypes.EventSessionStart,
	"Stop":         agenttypes.EventStop,
	"StopFailure":  agenttypes.EventStopFailure,
}

// ParseHookEvent parses raw Codex Hook JSON into a unified HookEvent.
//
// Codex hook events share these common fields:
//
//	hook_event_name  string   — event name
//	session_id       string   — session identifier
//	transcript_path  string   — path to conversation transcript
//	cwd              string   — current working directory
//	model            string   — active model slug
//
// Event-specific fields:
//
//	Stop:            last_assistant_message string  — Codex's final response
//	StopFailure:     error_type / error     string  — provider-specific failure reason
//	SessionStart:    source                 string  — startup | resume | clear | compact
func (c *Codex) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return agenttypes.HookEvent{}, fmt.Errorf("codex: parse hook event: %w", err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("codex: hook_event_name missing or not a string")
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("codex: unknown hook event %q", hookEventName)
	}

	e := agenttypes.HookEvent{
		AgentID: c.agentID,
		Event:   event,
		Raw:     raw,
	}

	if sid, ok := m["session_id"].(string); ok {
		e.SessionID = sid
	}

	switch event {
	case agenttypes.EventStop:
		if msg, ok := m["last_assistant_message"].(string); ok {
			e.Output = msg
		}
		e.OutputSource = "provider_field"
	case agenttypes.EventStopFailure:
		if et, ok := m["error_type"].(string); ok && et != "" {
			e.Error = et
		} else if msg, ok := m["error"].(string); ok && msg != "" {
			e.Error = msg
		}
		e.OutputSource = "none"
	}

	return e, nil
}
