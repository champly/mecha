package claude

import (
	"testing"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
)

func TestParseHookEvent_Stop(t *testing.T) {
	raw := []byte(`{"session_id":"d54db35e","hook_event_name":"Stop","last_assistant_message":"hello world"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-001", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventStop {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventStop)
	}
	if e.AgentID != "agent-001" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "agent-001")
	}
	if e.SessionID != "d54db35e" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "d54db35e")
	}
	if e.Output != "hello world" {
		t.Errorf("Output = %q, want %q", e.Output, "hello world")
	}
	if e.OutputSource != "provider_field" {
		t.Errorf("OutputSource = %q, want %q", e.OutputSource, "provider_field")
	}
}

func TestParseHookEvent_SessionStart(t *testing.T) {
	raw := []byte(`{"session_id":"abc123","hook_event_name":"SessionStart"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-002", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventSessionStart {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventSessionStart)
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
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-003", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventStopFailure {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventStopFailure)
	}
	if e.Error != "rate_limit" {
		t.Errorf("Error = %q, want %q", e.Error, "rate_limit")
	}
	if e.OutputSource != "none" {
		t.Errorf("OutputSource = %q, want %q", e.OutputSource, "none")
	}
}

func TestParseHookEvent_PostToolBatch(t *testing.T) {
	raw := []byte(`{"session_id":"xyz","hook_event_name":"PostToolBatch"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-004", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventPostToolBatch {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventPostToolBatch)
	}
}
