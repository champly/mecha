package provider

import (
	"strings"
	"testing"
)

func TestParseHook(t *testing.T) {
	eventMap := map[string]string{"Stop": EventStop}

	t.Run("resolves event and session id", func(t *testing.T) {
		e, err := ParseHook("claude", []byte(`{"hook_event_name":"Stop","session_id":"s1"}`), eventMap, nil)
		if err != nil {
			t.Fatalf("ParseHook: %v", err)
		}
		if e.Event != EventStop || e.SessionID != "s1" {
			t.Errorf("event = %+v, want Stop with session s1", e)
		}
	})

	t.Run("extract fills event-specific fields", func(t *testing.T) {
		e, err := ParseHook("claude", []byte(`{"hook_event_name":"Stop","last_assistant_message":"hi"}`), eventMap,
			func(m map[string]any, e *HookEvent) {
				if msg, ok := m["last_assistant_message"].(string); ok {
					e.Output = msg
				}
			})
		if err != nil {
			t.Fatalf("ParseHook: %v", err)
		}
		if e.Output != "hi" {
			t.Errorf("Output = %q, want %q", e.Output, "hi")
		}
	})

	t.Run("extracts shared turn prompt and source fields", func(t *testing.T) {
		e, err := ParseHook("codex", []byte(`{
			"hook_event_name":"Stop",
			"session_id":"s1",
			"turn_id":"turn-1",
			"prompt":"do work",
			"source":"clear",
			"notification_type":"permission_prompt",
			"message":"approve"
		}`), eventMap, nil)
		if err != nil {
			t.Fatalf("ParseHook: %v", err)
		}
		if e.TurnID != "turn-1" || e.Prompt != "do work" || e.Source != "clear" ||
			e.NotificationType != "permission_prompt" || e.Message != "approve" {
			t.Errorf("shared fields = %+v", e)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := ParseHook("claude", []byte(`{not json`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), "claude: parse hook event") {
			t.Errorf("expected prefixed parse error, got %v", err)
		}
	})

	t.Run("missing hook_event_name", func(t *testing.T) {
		_, err := ParseHook("codex", []byte(`{"session_id":"x"}`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), "codex: hook_event_name missing") {
			t.Errorf("expected prefixed missing-field error, got %v", err)
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		_, err := ParseHook("pi", []byte(`{"hook_event_name":"Nope"}`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), `pi: unknown hook event "Nope"`) {
			t.Errorf("expected prefixed unknown-event error, got %v", err)
		}
	})
}
