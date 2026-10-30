package provider

import (
	"encoding/json"
	"fmt"

	"github.com/champly/mecha/pkg/shellquote"
)

const (
	EventSessionStart = "SessionStart"
	EventPromptSubmit = "UserPromptSubmit"
	EventStop         = "Stop"
	EventStopFailure  = "StopFailure"
	EventInterrupt    = "Interrupt"
	EventInteraction  = "InteractionRequired"
)

type HookEvent struct {
	Event            string `json:"event"`
	SessionID        string `json:"session_id,omitempty"`
	TurnID           string `json:"turn_id,omitempty"`
	Prompt           string `json:"prompt,omitempty"`
	Source           string `json:"source,omitempty"`
	Output           string `json:"output,omitempty"`
	Error            string `json:"error,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	Message          string `json:"message,omitempty"`
}

// WebhookCommand returns the shell command line an agent hook runs to deliver
// events to the agentd webhook server.
func WebhookCommand(binary, addr string) string {
	return fmt.Sprintf("%s webhook --addr %s", shellquote.Quote(binary), shellquote.Quote(addr))
}

// ParseHook parses raw provider hook JSON: resolves hook_event_name through
// eventMap, then lets extract fill event-specific fields. prefix names the
// provider so errors in the shared webhook log stay attributable.
func ParseHook(prefix string, raw []byte, eventMap map[string]string, extract func(m map[string]any, e *HookEvent)) (HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return HookEvent{}, fmt.Errorf("%s: parse hook event: %w", prefix, err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return HookEvent{}, fmt.Errorf("%s: hook_event_name missing or not a string", prefix)
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return HookEvent{}, fmt.Errorf("%s: unknown hook event %q", prefix, hookEventName)
	}

	e := HookEvent{Event: event}
	if sid, ok := m["session_id"].(string); ok {
		e.SessionID = sid
	}
	if turnID, ok := m["turn_id"].(string); ok {
		e.TurnID = turnID
	}
	if prompt, ok := m["prompt"].(string); ok {
		e.Prompt = prompt
	}
	if source, ok := m["source"].(string); ok {
		e.Source = source
	}
	if notificationType, ok := m["notification_type"].(string); ok {
		e.NotificationType = notificationType
	}
	if message, ok := m["message"].(string); ok {
		e.Message = message
	}
	if extract != nil {
		extract(m, &e)
	}
	return e, nil
}
