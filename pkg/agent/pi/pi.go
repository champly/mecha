// Package pi implements the Pi coding agent type for mecha.
//
// Pi (pi.dev) has no built-in permission system, so no skip-permissions flag
// is needed.
package pi

import (
	"os/exec"
	"path/filepath"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/config"
)

const piBinary = "pi"

// Pi handles the Pi coding agent type for a specific role.
type Pi struct {
	agentprovider.Base
}

// New returns a Pi agent helper.
func New(ctx agentprovider.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agentprovider.Agent, error) {
	base := agentprovider.NewBase(ctx, cfg, runtime)
	if _, err := base.ResolveBinary(piBinary); err != nil {
		return nil, err
	}
	return &Pi{Base: base}, nil
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
	return agentprovider.WriteJSONFile(p.settingsPath(), p.settings())
}

func (p *Pi) settings() map[string]any {
	// Pi hooks take a single shell command string, so quote paths with spaces.
	webhookCmd := agentprovider.WebhookCommand(p.MechaBinary, p.WebhookAddr)

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
			agentprovider.EventStop: []any{
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
// Pi reads .pi/settings.json from its working directory, so the role directory
// is the CWD; the workspace stays reachable via parent-directory traversal.
func (p *Pi) Cmd() *exec.Cmd {
	cmd := p.NewAgentCmd(piBinary, nil, nil)
	cmd.Args = append(cmd.Args, "--append-system-prompt", p.Prompt)
	cmd.Dir = p.RoleDir
	return cmd
}
