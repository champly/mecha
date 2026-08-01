// Package codebuddy implements the CodeBuddy agent type for mecha.
package codebuddy

import (
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
	agenttypes.Base
}

// New returns a CodeBuddy agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	base := agenttypes.NewBase(ctx, cfg, runtime)
	if _, err := base.ResolveBinary(codebuddyBinary); err != nil {
		return nil, err
	}
	return &CodeBuddy{Base: base}, nil
}

func (c *CodeBuddy) promptPath() string {
	return filepath.Join(c.RoleDir, "CODEBUDDY.md")
}

func (c *CodeBuddy) settingsPath() string {
	return filepath.Join(c.RoleDir, "settings.json")
}

// Prepare creates the full CodeBuddy role directory.
func (c *CodeBuddy) Prepare() error {
	if err := c.PrepareRoleFile("CODEBUDDY.md"); err != nil {
		return err
	}
	return agenttypes.WriteJSONFile(c.settingsPath(), c.settings())
}

func (c *CodeBuddy) settings() map[string]any {
	// CodeBuddy command hooks only support a single shell-form `command`
	// string (run via $SHELL / Git Bash); there is no `args` exec form like
	// Claude Code, so the full command line must be one quoted string.
	// Reference: https://www.codebuddy.ai/docs/cli/hooks
	command := agenttypes.WebhookCommand(c.MechaBinary, c.WebhookAddr)

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
						"command": command,
					},
				},
			},
		}
	}
	return map[string]any{"hooks": hookEvents}
}

// Cmd builds the *exec.Cmd for launching the CodeBuddy agent.
func (c *CodeBuddy) Cmd() *exec.Cmd {
	cmd := c.NewAgentCmd(codebuddyBinary, defaultParams, defaultEnvs)
	cmd.Args = append(cmd.Args,
		"--append-system-prompt", c.Prompt,
		"--settings", c.settingsPath(),
	)
	return cmd
}
