package claude

import (
	"testing"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

func TestParseHookEvent_Stop(t *testing.T) {
	raw := []byte(`{"session_id":"d54db35e","hook_event_name":"Stop","last_assistant_message":"hello world"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agentprovider.EventStop {
		t.Errorf("Event = %q, want %q", e.Event, agentprovider.EventStop)
	}
	if e.SessionID != "d54db35e" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "d54db35e")
	}
	if e.Output != "hello world" {
		t.Errorf("Output = %q, want %q", e.Output, "hello world")
	}
}

func TestParseHookEvent_SessionStart(t *testing.T) {
	raw := []byte(`{"session_id":"abc123","hook_event_name":"SessionStart"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agentprovider.EventSessionStart {
		t.Errorf("Event = %q, want %q", e.Event, agentprovider.EventSessionStart)
	}
	if e.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "abc123")
	}
	if e.Output != "" {
		t.Errorf("Output should be empty for SessionStart, got %q", e.Output)
	}
}

func TestParseHookEvent_StopFailure(t *testing.T) {
	raw := []byte(`{"session_id":"fail1","hook_event_name":"StopFailure","error_type":"rate_limit"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agentprovider.EventStopFailure {
		t.Errorf("Event = %q, want %q", e.Event, agentprovider.EventStopFailure)
	}
	if e.Error != "rate_limit" {
		t.Errorf("Error = %q, want %q", e.Error, "rate_limit")
	}
}

func TestParseHookEvent_PromptSubmit(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")
	e, err := c.ParseHookEvent([]byte(`{"session_id":"s1","prompt_id":"prompt-1","hook_event_name":"UserPromptSubmit","prompt":"do work"}`))
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agentprovider.EventPromptSubmit {
		t.Errorf("Event = %q, want %q", e.Event, agentprovider.EventPromptSubmit)
	}
	if e.TurnID != "prompt-1" || e.Prompt != "do work" {
		t.Errorf("event = %+v", e)
	}
}
