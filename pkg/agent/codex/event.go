package codex

import (
	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

// eventMap converts Codex's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart": agentprovider.EventSessionStart,
	"Stop":         agentprovider.EventStop,
	"StopFailure":  agentprovider.EventStopFailure,
}

// ParseHookEvent parses raw Codex hook JSON into a unified HookEvent.
func (c *Codex) ParseHookEvent(raw []byte) (agentprovider.HookEvent, error) {
	return agentprovider.ParseHook("codex", raw, eventMap, func(m map[string]any, e *agentprovider.HookEvent) {
		switch e.Event {
		case agentprovider.EventStop:
			if msg, ok := m["last_assistant_message"].(string); ok && msg != "" {
				e.Output = msg
			}
		case agentprovider.EventStopFailure:
			if et, ok := m["error_type"].(string); ok && et != "" {
				e.Error = et
			} else if msg, ok := m["error"].(string); ok && msg != "" {
				e.Error = msg
			}
		}
	})
}
