package pi

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
		Name:  "pi-default",
		Type:  "pi",
		Model: "claude-sonnet-4-20250514",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
}

func testNew(workspace, roleDir, prompt string) *Pi {
	ctx := agenttypes.AgentContext{
		Workspace:   workspace,
		RoleDir:     roleDir,
		Prompt:      prompt,
		WebhookAddr: "127.0.0.1:12345",
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Pi)
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

	if err := c.PrepareRoleFile("PI.md"); err != nil {
		t.Fatalf("PrepareRoleFile() error: %v", err)
	}

	data, err := os.ReadFile(c.piMdPath())
	if err != nil {
		t.Fatalf("read PI.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("PI.md = %q, want %q", got, content)
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
	if !strings.Contains(string(data), c.WebhookAddr) {
		t.Errorf("settings.json missing webhook addr, got: %s", data)
	}
	for _, event := range []string{"SessionStart", "Stop"} {
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

	if _, err := os.Stat(c.piMdPath()); err != nil {
		t.Errorf("PI.md not created: %v", err)
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

	if cmd.Dir != c.RoleDir {
		t.Errorf("cmd.Dir = %q, want %q (Pi discovers .pi/ relative to CWD)", cmd.Dir, c.RoleDir)
	}

	if !slices.Contains(cmd.Args, "--append-system-prompt") {
		t.Errorf("--append-system-prompt should be present in args: %v", cmd.Args)
	}

	// Pi has no permission system, so no -y flag should be present.
	if slices.Contains(cmd.Args, "-y") || slices.Contains(cmd.Args, "--dangerously-skip-permissions") {
		t.Errorf("Pi should not have permission-skip flags: %v", cmd.Args)
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
		if ev.OutputSource != "provider_field" {
			t.Errorf("OutputSource = %q, want %q", ev.OutputSource, "provider_field")
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
