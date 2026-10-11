// Package codebuddy implements the CodeBuddy agent type for mecha.
package codebuddy

import (
	"os/exec"
	"path/filepath"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
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
	agentprovider.Base
}

// New returns a CodeBuddy agent helper.
func New(ctx agentprovider.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agentprovider.Agent, error) {
	base := agentprovider.NewBase(ctx, cfg, runtime)
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
	return agentprovider.WriteJSONFile(c.settingsPath(), c.settings())
}

func (c *CodeBuddy) settings() map[string]any {
	// CodeBuddy hooks take one shell-form `command` string instead of Claude
	// Code's command+args pair. See https://www.codebuddy.ai/docs/cli/hooks
	command := agentprovider.WebhookCommand(c.MechaBinary, c.WebhookAddr)

	hookEvents := map[string]any{}
	for _, event := range []string{
		agentprovider.EventSessionStart,
		agentprovider.EventStop,
		agentprovider.EventStopFailure,
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
