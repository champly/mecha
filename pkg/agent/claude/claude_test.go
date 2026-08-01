package claude

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
		Name:  "claude-default",
		Type:  "claude",
		Model: "claude-sonnet-4-5",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
}

func testNew(workspace, roleDir, prompt string) *Claude {
	ctx := agenttypes.AgentContext{
		Workspace:   workspace,
		RoleDir:     roleDir,
		Prompt:      prompt,
		WebhookAddr: "127.0.0.1:12345",
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Claude)
}

var _ agenttypes.Agent = (*Claude)(nil)

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

	if err := c.PrepareRoleFile("CLAUDE.md"); err != nil {
		t.Fatalf("PrepareRoleFile() error: %v", err)
	}

	data, err := os.ReadFile(c.claudeMdPath())
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("CLAUDE.md = %q, want %q", got, content)
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

	if _, err := os.Stat(c.claudeMdPath()); err != nil {
		t.Errorf("CLAUDE.md not created: %v", err)
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
	if !slices.Contains(cmd.Args, "--append-system-prompt-file") {
		t.Errorf("--append-system-prompt-file should be present in args: %v", cmd.Args)
	}

	for _, env := range cmd.Env {
		if strings.Contains(env, "CLAUDE_CONFIG_DIR") {
			t.Errorf("CLAUDE_CONFIG_DIR should not be in env, got: %v", cmd.Env)
		}
	}
}
