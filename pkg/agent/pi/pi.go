// Package pi implements the Pi coding agent type for mecha.
//
// Pi is an open-source terminal-native coding agent (pi.dev). Unlike Claude
// Code, Pi has no built-in permission system, so no --dangerously-skip-permissions
// or -y flag is needed. Pi discovers .pi/settings.json from its working
// directory and walks parent directories for AGENTS.md/CLAUDE.md context files.
package pi

import (
	"os/exec"
	"path/filepath"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const piBinary = "pi"

// Pi handles the Pi coding agent type for a specific role.
type Pi struct {
	agenttypes.Base
}

// New returns a Pi agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Pi{Base: agenttypes.NewBase(ctx, cfg, runtime)}, nil
}

func (p *Pi) piMdPath() string {
	return filepath.Join(p.RoleDir, "PI.md")
}

func (p *Pi) settingsPath() string {
	return filepath.Join(p.RoleDir, ".pi", "settings.json")
}

// Prepare creates the Pi role directory with PI.md and .pi/settings.json.
func (p *Pi) Prepare() error {
	if err := p.PrepareRoleFile("PI.md"); err != nil {
		return err
	}
	return agenttypes.WriteJSONFile(p.settingsPath(), p.settings())
}

func (p *Pi) settings() map[string]any {
	// Pi's hook command is a single shell command string (unlike Claude Code's
	// separate command+args array). Use shell quoting for paths with spaces.
	webhookCmd := agenttypes.WebhookCommand(p.MechaBinary, p.WebhookAddr)

	return map[string]any{
		"hooks": map[string]any{
			agenttypes.EventSessionStart: []any{
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
			agenttypes.EventStop: []any{
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
	}
}

// Cmd builds the *exec.Cmd for launching the Pi agent.
//
// Pi discovers .pi/settings.json from its working directory, which is set
// to the role directory (like Gemini). The workspace is still accessible
// via parent-directory traversal for AGENTS.md/CLAUDE.md context files.
func (p *Pi) Cmd() *exec.Cmd {
	cmd := p.NewAgentCmd(piBinary, nil, nil)
	cmd.Args = append(cmd.Args, "--append-system-prompt", p.Prompt)
	cmd.Dir = p.RoleDir
	return cmd
}
