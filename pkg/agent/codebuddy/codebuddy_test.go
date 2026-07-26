package codebuddy

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
		Name:  "codebuddy-default",
		Type:  "codebuddy",
		Model: "sonnet",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
}

func testNew(workspace, roleDir, prompt string) *CodeBuddy {
	ctx := agenttypes.AgentContext{
		Workspace:   workspace,
		RoleDir:     roleDir,
		Prompt:      prompt,
		WebhookAddr: "127.0.0.1:12345",
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*CodeBuddy)
}

func TestNew(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "test prompt")

	if c.workspace != "/ws" {
		t.Errorf("workspace = %q, want %q", c.workspace, "/ws")
	}
	if c.roleDir != "/ws/.mecha/roles/lead" {
		t.Errorf("roleDir = %q, want %q", c.roleDir, "/ws/.mecha/roles/lead")
	}
	if c.prompt != "test prompt" {
		t.Errorf("prompt = %q, want %q", c.prompt, "test prompt")
	}
}

func TestWritePrompt(t *testing.T) {
	content := "<your_assigned_role>\n你是一个测试角色。\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), content)

	if err := c.writePrompt(); err != nil {
		t.Fatalf("writePrompt() error: %v", err)
	}

	data, err := os.ReadFile(c.promptPath())
	if err != nil {
		t.Fatalf("read CODEBUDDY.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("CODEBUDDY.md = %q, want %q", got, content)
	}
}

func TestWriteSettings(t *testing.T) {
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), "prompt")

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
	if !strings.Contains(string(data), c.webhookAddr) {
		t.Errorf("settings.json missing webhook addr, got: %s", data)
	}
	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
		if !strings.Contains(string(data), event) {
			t.Errorf("settings.json missing hook event %q", event)
		}
	}
}

func TestPrepare(t *testing.T) {
	prompt := "<your_assigned_role>\n协调者\n</your_assigned_role>"
	dir := t.TempDir()
	c := testNew(dir, filepath.Join(dir, "role"), prompt)

	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	if _, err := os.Stat(c.promptPath()); err != nil {
		t.Errorf("CODEBUDDY.md not created: %v", err)
	}
	if _, err := os.Stat(c.settingsPath()); err != nil {
		t.Errorf("settings.json not created: %v", err)
	}
}

func TestCmd(t *testing.T) {
	dir := t.TempDir()
	roleDir := filepath.Join(dir, "role")
	c := testNew(dir, roleDir, "prompt")

	cmd := c.Cmd()

	if cmd.Dir != c.workspace {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.workspace)
	}

	if !slices.Contains(cmd.Args, "--settings") {
		t.Errorf("--settings should be present in args: %v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "--append-system-prompt") {
		t.Errorf("--append-system-prompt should be present in args: %v", cmd.Args)
	}

	for _, env := range cmd.Env {
		if strings.Contains(env, "CODEBUDDY_CONFIG_DIR") {
			t.Errorf("CODEBUDDY_CONFIG_DIR should not be in env, got: %v", cmd.Env)
		}
	}
}

func TestParseHookEvent(t *testing.T) {
	c := testNew("/ws", "/ws/.mecha/roles/lead", "prompt")

	t.Run("SessionStart", func(t *testing.T) {
		ev, err := c.ParseHookEvent([]byte(`{"hook_event_name":"SessionStart","session_id":"abc123"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Event != agenttypes.EventSessionStart {
			t.Errorf("event = %q, want %q", ev.Event, agenttypes.EventSessionStart)
		}
		if ev.SessionID != "abc123" {
			t.Errorf("session_id = %q, want %q", ev.SessionID, "abc123")
		}
	})

	t.Run("Stop", func(t *testing.T) {
		ev, err := c.ParseHookEvent([]byte(`{"hook_event_name":"Stop","last_assistant_message":"done!"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Event != agenttypes.EventStop {
			t.Errorf("event = %q, want %q", ev.Event, agenttypes.EventStop)
		}
		if ev.Output != "done!" {
			t.Errorf("output = %q, want %q", ev.Output, "done!")
		}
	})

	t.Run("StopFailure", func(t *testing.T) {
		ev, err := c.ParseHookEvent([]byte(`{"hook_event_name":"StopFailure","error_type":"tool_error"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Event != agenttypes.EventStopFailure {
			t.Errorf("event = %q, want %q", ev.Event, agenttypes.EventStopFailure)
		}
		if ev.Error != "tool_error" {
			t.Errorf("error = %q, want %q", ev.Error, "tool_error")
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		_, err := c.ParseHookEvent([]byte(`{"hook_event_name":"Unknown"}`))
		if err == nil {
			t.Fatal("expected error for unknown event")
		}
	})

	t.Run("missing hook_event_name", func(t *testing.T) {
		_, err := c.ParseHookEvent([]byte(`{}`))
		if err == nil {
			t.Fatal("expected error for missing hook_event_name")
		}
	})
}
