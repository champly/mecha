package gemini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

func testAgentConfig() config.AgentConfig {
	return config.AgentConfig{
		Name:  "gemini-default",
		Type:  "gemini",
		Model: "gemini-3-flash-preview",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", WebhookPort: "12345"}
}

func testNew(workspace, roleDir, agentID, prompt string) *Gemini {
	ctx := agenttypes.AgentContext{
		Workspace: workspace,
		RoleDir:   roleDir,
		Prompt:    prompt,
		AgentID:   agentID,
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Gemini)
}

func TestNew(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-001", "test prompt")

	if c.workspace != "/ws" {
		t.Errorf("workspace = %q, want %q", c.workspace, "/ws")
	}
	if c.roleDir != "/ws/.mecha/roles/lead" {
		t.Errorf("roleDir = %q, want %q", c.roleDir, "/ws/.mecha/roles/lead")
	}
	if c.agentID != "agent-001" {
		t.Errorf("agentID = %q, want %q", c.agentID, "agent-001")
	}
	if c.prompt != "test prompt" {
		t.Errorf("prompt = %q, want %q", c.prompt, "test prompt")
	}
}

func TestWritePrompt(t *testing.T) {
	content := "<your_assigned_role>\n你是一个测试角色。\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), "agent-001", content)

	if err := c.writePrompt(); err != nil {
		t.Fatalf("writePrompt() error: %v", err)
	}

	data, err := os.ReadFile(c.geminiMdPath())
	if err != nil {
		t.Fatalf("read GEMINI.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("GEMINI.md = %q, want %q", got, content)
	}
}

func TestWriteSettings(t *testing.T) {
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), "agent-123", "prompt")

	if err := c.writeSettings(); err != nil {
		t.Fatalf("writeSettings() error: %v", err)
	}

	data, err := os.ReadFile(c.settingsPath())
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	if !strings.Contains(string(data), c.mechaBinary) {
		t.Errorf("settings.json missing mecha path, got: %s", data)
	}
	if !strings.Contains(string(data), "agent-123") {
		t.Errorf("settings.json missing agent ID, got: %s", data)
	}
	for _, event := range []string{agenttypes.EventSessionStart, eventAfterAgent} {
		if !strings.Contains(string(data), event) {
			t.Errorf("settings.json missing hook event %q", event)
		}
	}
}

func TestPrepare(t *testing.T) {
	prompt := "<your_assigned_role>\n协调者\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), "agent-123", prompt)

	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	if _, err := os.Stat(c.geminiMdPath()); err != nil {
		t.Errorf("GEMINI.md not created: %v", err)
	}
	if _, err := os.Stat(c.settingsPath()); err != nil {
		t.Errorf("settings.json not created: %v", err)
	}
}

func TestCmd(t *testing.T) {
	dir := t.TempDir()
	roleDir := filepath.Join(dir, "role")
	c := testNew(dir, roleDir, "agent-001", "prompt")

	cmd := c.Cmd()

	if cmd.Dir != c.roleDir {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.roleDir)
	}
}

func TestParseHookEvent_AfterAgent(t *testing.T) {
	raw := []byte(`{"session_id":"d54db35e","hook_event_name":"AfterAgent","prompt_response":"hello world"}`)
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

func TestParseHookEvent_Unknown(t *testing.T) {
	raw := []byte(`{"hook_event_name":"BeforeTool"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-003", "prompt")

	_, err := c.ParseHookEvent(raw)
	if err == nil {
		t.Fatalf("ParseHookEvent() should error on unknown event")
	}
}
