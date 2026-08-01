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
	// Point Binary at the test binary itself so tests don't depend on the
	// real CLI being installed.
	bin, _ := os.Executable()
	return config.AgentConfig{
		Name:   "codex-default",
		Type:   "codex",
		Model:  "gpt-5.5",
		Binary: bin,
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
}

func testNew(workspace, roleDir, prompt string) *Codex {
	ctx := agenttypes.AgentContext{
		Workspace:   workspace,
		RoleDir:     roleDir,
		Prompt:      prompt,
		WebhookAddr: "127.0.0.1:12345",
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Codex)
}

var _ agenttypes.Agent = (*Codex)(nil)

func TestNew(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "test prompt")

	if c.Workspace != "/ws" {
		t.Errorf("workspace = %q, want %q", c.Workspace, "/ws")
	}
	if c.RoleDir != "/ws/.mecha/roles/lead" {
		t.Errorf("roleDir = %q, want %q", c.RoleDir, "/ws/.mecha/roles/lead")
	}
	if c.Prompt != "test prompt" {
		t.Errorf("prompt = %q, want %q", c.Prompt, "test prompt")
	}
}

func TestWritePrompt(t *testing.T) {
	content := "<your_assigned_role>\n你是一个测试角色。\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), content)

	if err := c.PrepareRoleFile("AGENTS.md"); err != nil {
		t.Fatalf("PrepareRoleFile() error: %v", err)
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
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")
	args := c.configArgs()

	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
		if !slices.Contains(args, "hooks."+event+"=[{hooks=[{command=\"mecha\",args=[\"webhook\",\"--addr\",\"127.0.0.1:12345\"]}]}]") {
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
	c := testNew(dir, filepath.Join(dir, "role"), prompt)

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
	c := testNew(dir, roleDir, "prompt")

	cmd := c.Cmd()

	if cmd.Dir != c.Workspace {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.Workspace)
	}

	if !slices.Contains(cmd.Args, "--cd") {
		t.Errorf("--cd should be present in args: %v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, c.Workspace) {
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
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	e, err := c.ParseHookEvent(raw)
	if err != nil {
		t.Fatalf("ParseHookEvent() error: %v", err)
	}
	if e.Event != agenttypes.EventStop {
		t.Errorf("Event = %q, want %q", e.Event, agenttypes.EventStop)
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
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

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
}

func TestParseHookEvent_Unknown(t *testing.T) {
	raw := []byte(`{"hook_event_name":"PostToolUse"}`)
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	_, err := c.ParseHookEvent(raw)
	if err == nil {
		t.Fatalf("ParseHookEvent() should error on unknown event")
	}
}
