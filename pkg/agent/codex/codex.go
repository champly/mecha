// Package codex implements the Codex agent type for mecha.
package codex

import (
	"os/exec"
	"path/filepath"
	"strconv"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/config"
)

const codexBinary = "codex"

var defaultParams = map[string]any{
	"dangerously-bypass-approvals-and-sandbox": true,
	"dangerously-bypass-hook-trust":            true,
}

// Codex handles the Codex CLI agent type for a specific role.
type Codex struct {
	agentprovider.Base
}

// New returns a Codex agent helper.
func New(ctx agentprovider.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agentprovider.Agent, error) {
	base := agentprovider.NewBase(ctx, cfg, runtime)
	if _, err := base.ResolveBinary(codexBinary); err != nil {
		return nil, err
	}
	return &Codex{Base: base}, nil
}

func (c *Codex) agentsMdPath() string {
	return filepath.Join(c.RoleDir, "AGENTS.md")
}

// Prepare creates the role-specific instructions file consumed by Codex.
func (c *Codex) Prepare() error {
	return c.PrepareRoleFile("AGENTS.md")
}

// Cmd builds the *exec.Cmd for launching the Codex agent.
func (c *Codex) Cmd() *exec.Cmd {
	cmd := c.NewAgentCmd(codexBinary, defaultParams, nil)
	cmd.Args = append(cmd.Args, c.configArgs()...)
	cmd.Args = append(cmd.Args, "--cd", c.Workspace)
	return cmd
}

func (c *Codex) configArgs() []string {
	hookCommand := agentprovider.WebhookCommand(c.MechaBinary, c.WebhookAddr)
	args := []string{
		"--config", "model_instructions_file=" + strconv.Quote(c.agentsMdPath()),
	}
	for _, event := range []string{agentprovider.EventSessionStart, agentprovider.EventStop, agentprovider.EventStopFailure} {
		args = append(args, "--config", "hooks."+event+"="+inlineHookConfig(hookCommand))
	}
	return args
}

func inlineHookConfig(command string) string {
	return `[{hooks=[{type="command",command=` + strconv.Quote(command) + `}]}]`
}
