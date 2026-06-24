// Package claude implements the Claude agent type for mecha.
package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const claudeBinary = "claude"

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
	workspace   string
	roleDir     string
	agentID     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookPort string
}

// New returns a Claude agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Claude{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		agentID:     ctx.AgentID,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookPort: runtime.WebhookPort,
	}, nil
}

// ID returns the agent's unique identifier.
func (c *Claude) ID() string {
	return c.agentID
}

// claudeMdPath returns the path to the agent's CLAUDE.md file.
func (c *Claude) claudeMdPath() string {
	return filepath.Join(c.roleDir, "CLAUDE.md")
}

// settingsPath returns the path to the agent's settings.json file.
func (c *Claude) settingsPath() string {
	return filepath.Join(c.roleDir, "settings.json")
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

	if err := os.WriteFile(c.claudeMdPath(), []byte(c.prompt), 0o644); err != nil {
		return fmt.Errorf("claude: write CLAUDE.md: %w", err)
	}
	return nil
}

func (c *Claude) writeSettings() error {
	if err := os.MkdirAll(c.roleDir, 0o755); err != nil {
		return fmt.Errorf("claude: create role dir: %w", err)
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
						"command": c.mechaBinary,
						"args":    []string{"webhook", "--id", c.agentID, "--port", c.webhookPort},
					},
				},
			},
		}
	}
	settings := map[string]any{"hooks": hookEvents}

	f, err := os.Create(c.settingsPath())
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

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := params[k]
		if b, ok := v.(bool); ok && b {
			args = append(args, "--"+k)
		} else {
			args = append(args, "--"+k, fmt.Sprint(v))
		}
	}

	args = append(args,
		"--settings", c.settingsPath(),
		"--append-system-prompt-file", c.claudeMdPath(),
	)

	binary := c.cfg.Binary
	if binary == "" {
		binary = claudeBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.workspace
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
