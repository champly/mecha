// Package codebuddy implements the CodeBuddy agent type for mecha.
package codebuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const codebuddyBinary = "codebuddy"

var (
	defaultParams = map[string]any{
		"dangerously-skip-permissions": true,
	}
	defaultEnvs = map[string]string{
		"CODEBUDDY_CODE_MAX_OUTPUT_TOKENS": "8192",
	}
)

// CodeBuddy handles the CodeBuddy agent type for a specific role.
type CodeBuddy struct {
	workspace   string
	roleDir     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookAddr string
}

// New returns a CodeBuddy agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &CodeBuddy{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookAddr: ctx.WebhookAddr,
	}, nil
}

func (c *CodeBuddy) promptPath() string {
	return filepath.Join(c.roleDir, "CODEBUDDY.md")
}

func (c *CodeBuddy) settingsPath() string {
	return filepath.Join(c.roleDir, "settings.json")
}

// Prepare creates the full CodeBuddy role directory.
func (c *CodeBuddy) Prepare() error {
	if err := c.writePrompt(); err != nil {
		return err
	}
	return c.writeSettings()
}

func (c *CodeBuddy) writePrompt() error {
	if err := os.MkdirAll(c.roleDir, 0o755); err != nil {
		return fmt.Errorf("codebuddy: create dir %q: %w", c.roleDir, err)
	}

	if err := os.WriteFile(c.promptPath(), []byte(c.prompt), 0o644); err != nil {
		return fmt.Errorf("codebuddy: write CODEBUDDY.md: %w", err)
	}
	return nil
}

func (c *CodeBuddy) writeSettings() error {
	if err := os.MkdirAll(c.roleDir, 0o755); err != nil {
		return fmt.Errorf("codebuddy: create role dir: %w", err)
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
						"args":    []string{"webhook", "--addr", c.webhookAddr},
					},
				},
			},
		}
	}
	settings := map[string]any{"hooks": hookEvents}

	f, err := os.Create(c.settingsPath())
	if err != nil {
		return fmt.Errorf("codebuddy: create settings.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return fmt.Errorf("codebuddy: encode settings.json: %w", err)
	}
	return nil
}

// Cmd builds the *exec.Cmd for launching the CodeBuddy agent.
func (c *CodeBuddy) Cmd() *exec.Cmd {
	args := []string{}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}

	args = append(args, agenttypes.BuildArgs(c.cfg.Params, defaultParams)...)
	args = append(
		args,
		"--append-system-prompt", c.prompt,
		"--settings", c.settingsPath(),
	)

	binary := c.cfg.Binary
	if binary == "" {
		binary = codebuddyBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.workspace
	cmd.Env = agenttypes.BuildEnv(c.cfg.Envs, defaultEnvs)
	return cmd
}
