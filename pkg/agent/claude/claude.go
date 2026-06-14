// Package claude implements the Claude agent type for mecha.
package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

var (
	defaultParams = map[string]any{
		"dangerously-skip-permissions": true,
	}
	defaultEnvs = map[string]string{
		"BASH_DEFAULT_TIMEOUT_MS": "1200000",
	}
)

// Claude handles the Claude Code agent type for a specific role.
type Claude struct {
	roleDir string
	agentID string
	prompt  string

	cfg config.AgentConfig
}

// New returns a Claude agent helper.
func New(roleDir, agentID, prompt string, cfg config.AgentConfig) (agenttypes.Agent, error) {
	return &Claude{roleDir: roleDir, agentID: agentID, prompt: prompt, cfg: cfg}, nil
}

// ID returns the agent's unique identifier.
func (c *Claude) ID() string {
	return c.agentID
}

// Prepare creates the full Claude Code role directory.
func (c *Claude) Prepare() error {
	if err := c.writePrompt(); err != nil {
		return err
	}
	return c.writeSettings()
}

func (c *Claude) writePrompt() error {
	if err := os.MkdirAll(c.roleDir, 0o755); err != nil {
		return fmt.Errorf("claude: create dir %q: %w", c.roleDir, err)
	}

	path := filepath.Join(c.roleDir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(c.prompt), 0o644); err != nil {
		return fmt.Errorf("claude: write CLAUDE.md: %w", err)
	}
	return nil
}

func (c *Claude) writeSettings() error {
	dotClaude := filepath.Join(c.roleDir, ".claude")
	if err := os.MkdirAll(dotClaude, 0o755); err != nil {
		return fmt.Errorf("claude: create .claude dir: %w", err)
	}

	hookEvents := map[string]any{}
	for _, event := range []string{
		agenttypes.EventSessionStart,
		agenttypes.EventStop,
		agenttypes.EventStopFailure,
	} {
		hookEvents[event] = []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": config.MechaBinary,
						"args":    []string{"webhook", "--id", c.agentID, "--port", config.WebhookPort},
					},
				},
			},
		}
	}
	settings := map[string]any{"hooks": hookEvents}

	settingsPath := filepath.Join(dotClaude, "settings.json")
	f, err := os.Create(settingsPath)
	if err != nil {
		return fmt.Errorf("claude: create settings.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return fmt.Errorf("claude: encode settings.json: %w", err)
	}
	return nil
}

// Cmd builds the *exec.Cmd for launching the Claude Code agent.
func (c *Claude) Cmd() *exec.Cmd {
	args := []string{}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}

	params := merge(c.cfg.Params, defaultParams)
	for k, v := range params {
		if b, ok := v.(bool); ok && b {
			args = append(args, "--"+k)
		} else {
			args = append(args, "--"+k, fmt.Sprint(v))
		}
	}

	// Pre-authorize the role directory so Claude Code skips the trust dialog.
	args = append(args, "--add-dir", c.roleDir)

	cmd := exec.Command("claude", args...)
	cmd.Dir = c.roleDir
	for k, v := range merge(c.cfg.Envs, defaultEnvs) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

func merge[M ~map[K]V, K comparable, V any](user, defaults M) M {
	if len(defaults) == 0 {
		return maps.Clone(user)
	}

	r := maps.Clone(defaults)
	maps.Copy(r, user)
	return r
}
