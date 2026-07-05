package codex

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

func testAgentConfig() config.AgentConfig {
	return config.AgentConfig{
		Name:  "codex-default",
		Type:  "codex",
		Model: "gpt-5.5",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", WebhookPort: "12345"}
}

func testNew(workspace, roleDir, agentID, prompt string) *Codex {
	ctx := agenttypes.AgentContext{
		Workspace: workspace,
		RoleDir:   roleDir,
		Prompt:    prompt,
		AgentID:   agentID,
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Codex)
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

	data, err := os.ReadFile(c.agentsMdPath())
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("AGENTS.md = %q, want %q", got, content)
	}
}

func TestWriteConfig(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-123", "prompt")
	args := c.configArgs()

	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
		if !slices.Contains(args, "hooks."+event+"=[{hooks=[{command=\"mecha\",args=[\"webhook\",\"--id\",\"agent-123\",\"--port\",\"12345\"]}]}]") {
			t.Errorf("config args missing hook event %q: %v", event, args)
		}
	}
	if !slices.Contains(args, "model_instructions_file=\"/ws/.mecha/roles/lead/AGENTS.md\"") {
		t.Errorf("config args missing model_instructions_file override: %v", args)
	}
}

func TestPrepare(t *testing.T) {
	prompt := "<your_assigned_role>\n协调者\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), "agent-123", prompt)

	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	if _, err := os.Stat(c.agentsMdPath()); err != nil {
		t.Errorf("AGENTS.md not created: %v", err)
	}
}

func TestCmd(t *testing.T) {
	dir := t.TempDir()
	roleDir := filepath.Join(dir, "role")
	c := testNew(dir, roleDir, "agent-001", "prompt")

	cmd := c.Cmd()

	if cmd.Dir != c.workspace {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.workspace)
	}

	if !slices.Contains(cmd.Args, "--cd") {
		t.Errorf("--cd should be present in args: %v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, c.workspace) {
		t.Errorf("workspace should be present in args: %v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "--config") {
		t.Errorf("--config should be present in args: %v", cmd.Args)
	}

	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "CODEX_HOME=") {
			t.Errorf("cmd.Env should not override Codex config root: %v", cmd.Env)
		}
	}
}

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
	raw := []byte(`{"session_id":"deadbeef","hook_event_name":"StopFailure","error_type":"overloaded"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-004", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventStopFailure {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventStopFailure)
	}
	if e.SessionID != "deadbeef" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "deadbeef")
	}
	if e.Error != "overloaded" {
		t.Errorf("Error = %q, want %q", e.Error, "overloaded")
	}
	if e.OutputSource != "none" {
		t.Errorf("OutputSource = %q, want %q", e.OutputSource, "none")
	}
}

func TestParseHookEvent_Unknown(t *testing.T) {
	raw := []byte(`{"hook_event_name":"PostToolUse"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "agent-003", "prompt")

	_, err := c.ParseHookEvent(raw)
	if err == nil {
		t.Fatalf("ParseHookEvent() should error on unknown event")
	}
}
