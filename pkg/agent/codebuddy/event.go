package codebuddy

import (
	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

// eventMap converts CodeBuddy's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart":     agentprovider.EventSessionStart,
	"UserPromptSubmit": agentprovider.EventPromptSubmit,
	"Stop":             agentprovider.EventStop,
	"StopFailure":      agentprovider.EventStopFailure,
	"Notification":     agentprovider.EventInteraction,
}

// ParseHookEvent parses raw CodeBuddy hook JSON into a unified HookEvent.
// Reference: https://www.codebuddy.ai/docs/cli/hooks
func (c *CodeBuddy) ParseHookEvent(raw []byte) (agentprovider.HookEvent, error) {
	return agentprovider.ParseHook("codebuddy", raw, eventMap, func(m map[string]any, e *agentprovider.HookEvent) {
		if e.TurnID == "" {
			if generationID, ok := m["generation_id"].(string); ok {
				e.TurnID = generationID
			}
		}
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
