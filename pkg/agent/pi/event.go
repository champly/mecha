package pi

import (
	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

// eventMap converts Pi's hook_event_name values to internal event constants.
// Pi has no StopFailure event; process crashes are caught by agentd.
var eventMap = map[string]string{
	"SessionStart": agentprovider.EventSessionStart,
	"Stop":         agentprovider.EventStop,
}

// ParseHookEvent parses raw Pi hook JSON into a unified HookEvent.
func (p *Pi) ParseHookEvent(raw []byte) (agentprovider.HookEvent, error) {
	return agentprovider.ParseHook("pi", raw, eventMap, func(m map[string]any, e *agentprovider.HookEvent) {
		switch e.Event {
		case agentprovider.EventStop:
			if msg, ok := m["last_assistant_message"].(string); ok && msg != "" {
				e.Output = msg
			}
		}
	})
}
