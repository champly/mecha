package gemini

import (
	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

// eventAfterAgent carries the assistant response and maps to EventStop.
const eventAfterAgent = "AfterAgent"

// eventMap converts Gemini CLI's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart":  agentprovider.EventSessionStart,
	eventAfterAgent: agentprovider.EventStop,
}

// ParseHookEvent parses raw Gemini CLI hook JSON into a unified HookEvent.
func (g *Gemini) ParseHookEvent(raw []byte) (agentprovider.HookEvent, error) {
	return agentprovider.ParseHook("gemini", raw, eventMap, func(m map[string]any, e *agentprovider.HookEvent) {
		switch e.Event {
		case agentprovider.EventStop:
			if msg, ok := m["prompt_response"].(string); ok && msg != "" {
				e.Output = msg
			}
		}
	})
}
