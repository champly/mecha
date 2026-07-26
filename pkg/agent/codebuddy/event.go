package codebuddy

import (
	"encoding/json"
	"fmt"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventMap converts CodeBuddy's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart": agenttypes.EventSessionStart,
	"Stop":         agenttypes.EventStop,
	"StopFailure":  agenttypes.EventStopFailure,
}

// ParseHookEvent parses raw CodeBuddy hook JSON into a unified HookEvent.
// CodeBuddy shares the same hook event format as Claude Code.
func (c *CodeBuddy) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return agenttypes.HookEvent{}, fmt.Errorf("codebuddy: parse hook event: %w", err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("codebuddy: hook_event_name missing or not a string")
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("codebuddy: unknown hook event %q", hookEventName)
	}

	e := agenttypes.HookEvent{Event: event}

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
		if et, ok := m["error_type"].(string); ok {
			e.Error = et
		}
		e.OutputSource = "none"
	}

	return e, nil
}
