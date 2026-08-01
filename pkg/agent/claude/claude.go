// Package claude implements the Claude agent type for mecha.
package claude

import (
	"os/exec"
	"path/filepath"

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
	agenttypes.Base
}

// New returns a Claude agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Claude{Base: agenttypes.NewBase(ctx, cfg, runtime)}, nil
}

func (c *Claude) claudeMdPath() string {
	return filepath.Join(c.RoleDir, "CLAUDE.md")
}

func (c *Claude) settingsPath() string {
	return filepath.Join(c.RoleDir, "settings.json")
}

// Prepare creates the full Claude Code role directory.
func (c *Claude) Prepare() error {
	if err := c.PrepareRoleFile("CLAUDE.md"); err != nil {
		return err
	}
	return agenttypes.WriteJSONFile(c.settingsPath(), c.settings())
}

func (c *Claude) settings() map[string]any {
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
						"command": c.MechaBinary,
						"args":    []string{"webhook", "--addr", c.WebhookAddr},
					},
				},
			},
		}
	}
	return map[string]any{"hooks": hookEvents}
}

// Cmd builds the *exec.Cmd for launching the Claude Code agent.
func (c *Claude) Cmd() *exec.Cmd {
	cmd := c.NewAgentCmd(claudeBinary, defaultParams, defaultEnvs)
	cmd.Args = append(cmd.Args,
		"--settings", c.settingsPath(),
		"--append-system-prompt-file", c.claudeMdPath(),
	)
	return cmd
}
