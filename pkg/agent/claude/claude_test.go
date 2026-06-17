package claude

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
		Name:  "claude-default",
		Type:  "claude",
		Model: "claude-sonnet-4-5",
	}
}

func testRuntime() config.Runtime {
	return config.Runtime{MechaBinary: "mecha", WebhookPort: "12345"}
}

func testNew(dir, agentID, prompt string) *Claude {
	a, _ := New(dir, agentID, prompt, testAgentConfig(), testRuntime())
	return a.(*Claude)
}

func TestWritePrompt(t *testing.T) {
	content := "<your_assigned_role>\n你是一个测试角色。\n</your_assigned_role>"
	c := testNew(t.TempDir(), "agent-001", content)

	if err := c.writePrompt(); err != nil {
		t.Fatalf("writePrompt() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.roleDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if got := string(data); got != content {
		t.Errorf("CLAUDE.md = %q, want %q", got, content)
	}
}

func TestWriteSettings(t *testing.T) {
	c := testNew(t.TempDir(), "agent-123", "prompt")

	if err := c.writeSettings(); err != nil {
		t.Fatalf("writeSettings() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(c.roleDir, ".claude", "settings.json"))
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
	c := testNew(t.TempDir(), "agent-123", prompt)

	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(c.roleDir, "CLAUDE.md")); err != nil {
		t.Errorf("CLAUDE.md not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.roleDir, ".claude", "settings.json")); err != nil {
		t.Errorf("settings.json not created: %v", err)
	}
}
