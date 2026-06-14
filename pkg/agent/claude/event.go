package claude

import (
	"encoding/json"
	"fmt"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

// eventMap converts Claude's hook_event_name values to internal event constants.
var eventMap = map[string]string{
	"SessionStart":  agenttypes.EventSessionStart,
	"PostToolBatch": agenttypes.EventPostToolBatch,
	"Stop":          agenttypes.EventStop,
	"StopFailure":   agenttypes.EventStopFailure,
}

// ParseHookEvent parses raw Claude Hook JSON into a unified HookEvent.
//
// Reference: https://code.claude.com/docs/en/hooks
//
// Claude Code hook events share these common fields:
//
//	hook_event_name  string   — "SessionStart" | "PostToolBatch" | "Stop" | "StopFailure"
//	session_id       string   — session identifier
//	transcript_path  string   — path to conversation transcript
//	cwd              string   — current working directory
//
// Event-specific fields:
//
//	Stop:            last_assistant_message string  — Claude's final response
//	StopFailure:     error_type             string  — rate_limit | overloaded | authentication_failed | ...
//	SessionStart:    source                 string  — startup | resume | clear | compact
func (c *Claude) ParseHookEvent(raw []byte) (agenttypes.HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return agenttypes.HookEvent{}, fmt.Errorf("claude: parse hook event: %w", err)
	}

	hookEventName, _ := m["hook_event_name"].(string)

	event, ok := eventMap[hookEventName]
	if !ok {
		return agenttypes.HookEvent{}, fmt.Errorf("claude: unknown hook event %q", hookEventName)
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
		if et, ok := m["error_type"].(string); ok {
			e.Error = et
		}
		e.OutputSource = "none"
	}

	return e, nil
}
