package pi

import (
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
	return agenttypes.ParseHook("pi", raw, eventMap, func(m map[string]any, e *agenttypes.HookEvent) {
		switch e.Event {
		case agenttypes.EventStop:
			if msg, ok := m["last_assistant_message"].(string); ok && msg != "" {
				e.Output = msg
			}
		}
	})
}
