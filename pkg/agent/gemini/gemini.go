// Package gemini implements the Gemini CLI agent type for mecha.
package gemini

import (
	"os/exec"
	"path/filepath"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/config"
)

const geminiBinary = "gemini"

var defaultParams = map[string]any{
	"yolo": true,
}

// Gemini handles the Gemini CLI agent type for a specific role.
type Gemini struct {
	agentprovider.Base
}

// New returns a Gemini agent helper.
func New(ctx agentprovider.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agentprovider.Agent, error) {
	base := agentprovider.NewBase(ctx, cfg, runtime)
	if _, err := base.ResolveBinary(geminiBinary); err != nil {
		return nil, err
	}
	return &Gemini{Base: base}, nil
}

func (g *Gemini) geminiMdPath() string {
	return filepath.Join(g.RoleDir, "GEMINI.md")
}

func (g *Gemini) settingsPath() string {
	return filepath.Join(g.RoleDir, "settings.json")
}

// Prepare creates the full Gemini CLI role directory.
func (g *Gemini) Prepare() error {
	if err := g.PrepareRoleFile("GEMINI.md"); err != nil {
		return err
	}
	return agentprovider.WriteJSONFile(g.settingsPath(), g.settings())
}

func (g *Gemini) settings() map[string]any {
	webhookCmd := agentprovider.WebhookCommand(g.MechaBinary, g.WebhookAddr)

	return map[string]any{
		"hooks": map[string]any{
			agentprovider.EventSessionStart: []any{
				map[string]any{
					"matcher": "startup",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": webhookCmd,
						},
					},
				},
			},
			eventBeforeAgent: []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": webhookCmd,
						},
					},
				},
			},
			eventAfterAgent: []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": webhookCmd,
						},
					},
				},
			},
			"Notification": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": webhookCmd,
						},
					},
				},
			},
		},
		"context": map[string]any{
			"loadMemoryFromIncludeDirectories": true,
		},
		"tools": map[string]any{
			"shell": map[string]any{
				"inactivityTimeout": 3600,
			},
		},
	}
}

// Cmd builds the *exec.Cmd for launching the Gemini CLI agent.
func (g *Gemini) Cmd() *exec.Cmd {
	cmd := g.NewAgentCmd(geminiBinary, defaultParams, nil)
	// Keep the project as the working directory. The role directory is added as
	// an included workspace so its GEMINI.md is loaded as role context, while
	// generated settings are supplied explicitly from the global mecha dir.
	cmd.Args = append(cmd.Args, "--include-directories", g.RoleDir)
	cmd.Env = agentprovider.SetEnv(cmd.Env, "GEMINI_CLI_SYSTEM_SETTINGS_PATH", g.settingsPath())
	return cmd
}
