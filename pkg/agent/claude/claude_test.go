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
	return config.Runtime{MechaBinary: "mecha", WebhookPort: "12345"}
}

func testNew(workspace, roleDir, agentID, prompt string) *Claude {
	ctx := agenttypes.AgentContext{
		Workspace: workspace,
		RoleDir:   roleDir,
		Prompt:    prompt,
		AgentID:   agentID,
	}
	a, _ := New(ctx, testAgentConfig(), testRuntime())
	return a.(*Claude)
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
	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
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
	c := testNew(dir, roleDir, "agent-001", "prompt")

	cmd := c.Cmd()

	if cmd.Dir != c.workspace {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, c.workspace)
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
