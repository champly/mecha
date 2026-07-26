package pi

import (
	"encoding/json"
	"fmt"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventMap converts Pi's hook_event_name values to internal event constants.
// Pi does not have a StopFailure event (like Gemini); agent process crashes
// are caught by agentd's waitAgent.
var eventMap = map[string]string{
	"SessionStart": agenttypes.EventSessionStart,
	"Stop":         agenttypes.EventStop,
}

// ParseHookEvent parses raw Pi hook JSON into a unified HookEvent.
// Pi's hook format follows the same pattern as Claude Code (hook_event_name field).
func (p *Pi) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return agenttypes.HookEvent{}, fmt.Errorf("pi: parse hook event: %w", err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("pi: hook_event_name missing or not a string")
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("pi: unknown hook event %q", hookEventName)
	}

	e := agenttypes.HookEvent{
		Event: event,
		Raw:   raw,
	}

	if sid, ok := m["session_id"].(string); ok {
		e.SessionID = sid
	}

	// Pi's Stop event may carry last_assistant_message (Claude Code compat) or
	// tool_response (Pi-native); check both.
	switch event {
	case agenttypes.EventStop:
		if msg, ok := m["last_assistant_message"].(string); ok {
			e.Output = msg
			e.OutputSource = "provider_field"
		}
	}

	return e, nil
}
