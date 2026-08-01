package codex

import (
	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventMap converts Codex's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart": agenttypes.EventSessionStart,
	"Stop":         agenttypes.EventStop,
	"StopFailure":  agenttypes.EventStopFailure,
}

// ParseHookEvent parses raw Codex hook JSON into a unified HookEvent.
func (c *Codex) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	return agenttypes.ParseHook("codex", raw, eventMap, func(m map[string]any, e *agenttypes.HookEvent) {
		switch e.Event {
		case agenttypes.EventStop:
			if msg, ok := m["last_assistant_message"].(string); ok && msg != "" {
				e.Output = msg
				e.OutputSource = "provider_field"
			}
		case agenttypes.EventStopFailure:
			if et, ok := m["error_type"].(string); ok && et != "" {
				e.Error = et
			} else if msg, ok := m["error"].(string); ok && msg != "" {
				e.Error = msg
			}
			e.OutputSource = "none"
		}
	})
}
