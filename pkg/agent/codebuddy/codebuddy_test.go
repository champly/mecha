package codebuddy

import (
	"encoding/json"
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

	if err := c.PrepareRoleFile("CODEBUDDY.md"); err != nil {
		t.Fatalf("PrepareRoleFile() error: %v", err)
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

	if err := agenttypes.WriteJSONFile(c.settingsPath(), c.settings()); err != nil {
		t.Fatalf("WriteJSONFile() error: %v", err)
	}

	data, err := os.ReadFile(c.settingsPath())
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	if !strings.Contains(string(data), c.MechaBinary) {
		t.Errorf("settings.json missing mecha path, got: %s", data)
	}
	if !strings.Contains(string(data), "webhook --addr") {
		t.Errorf("settings.json missing webhook command, got: %s", data)
	}
	if !strings.Contains(string(data), c.WebhookAddr) {
		t.Errorf("settings.json missing webhook addr, got: %s", data)
	}
	// CodeBuddy command hooks have no exec-form `args` field; the command
	// must be a single shell string.
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	hooks := decoded["hooks"].(map[string]any)
	for name, event := range hooks {
		hook := event.([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		if _, ok := hook["args"]; ok {
			t.Errorf("hook %q has unsupported args field: %v", name, hook)
		}
		cmd, ok := hook["command"].(string)
		if !ok || !strings.Contains(cmd, c.MechaBinary) || !strings.Contains(cmd, "webhook --addr "+agenttypes.QuoteShell(c.WebhookAddr)) {
			t.Errorf("hook %q command = %q, want shell string with mecha webhook", name, cmd)
		}
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

	if cmd.Dir != c.Workspace {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.Workspace)
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

	t.Run("Stop with last_assistant_message", func(t *testing.T) {
		payload := `{"session_id":"df88915a-be69-47ef-83c1-d8a31b33b9c8","transcript_path":"/tmp/x.jsonl","cwd":"/tmp/mecha","hook_event_name":"Stop","stop_hook_active":false,"agent_type":"cli","last_assistant_message":"当前系统时间：2026年7月28日","background_tasks":[],"session_crons":[],"permission_mode":"bypassPermissions","client":"CLI","version":"2.127.3","model":"glm"}`
		ev, err := c.ParseHookEvent([]byte(payload))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Event != agenttypes.EventStop {
			t.Errorf("event = %q, want %q", ev.Event, agenttypes.EventStop)
		}
		if ev.Output != "当前系统时间：2026年7月28日" {
			t.Errorf("output = %q, want %q", ev.Output, "当前系统时间：2026年7月28日")
		}
		if ev.OutputSource != "provider_field" {
			t.Errorf("output_source = %q, want %q", ev.OutputSource, "provider_field")
		}
	})

	t.Run("Stop without last_assistant_message", func(t *testing.T) {
		// [118;1:3uField absent → Output stays empty (no transcript fallback).
		ev, err := c.ParseHookEvent([]byte(`{"hook_event_name":"Stop","stop_hook_active":false,"transcript_path":"/tmp/x.jsonl"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Output != "" {
			t.Errorf("output = %q, want empty", ev.Output)
		}
		if ev.OutputSource != "" {
			t.Errorf("output_source = %q, want empty", ev.OutputSource)
		}
	})

	t.Run("Stop with empty last_assistant_message", func(t *testing.T) {
		// Field present but empty string → treat as no output.
		ev, err := c.ParseHookEvent([]byte(`{"hook_event_name":"Stop","last_assistant_message":""}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ev.Output != "" {
			t.Errorf("output = %q, want empty", ev.Output)
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
