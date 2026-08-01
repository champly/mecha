// Package codex implements the Codex agent type for mecha.
package codex

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const codexBinary = "codex"

var defaultParams = map[string]any{
	"dangerously-bypass-approvals-and-sandbox": true,
}

// Codex handles the Codex CLI agent type for a specific role.
type Codex struct {
	agenttypes.Base
}

// New returns a Codex agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Codex{Base: agenttypes.NewBase(ctx, cfg, runtime)}, nil
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
	hookArgs := []string{"webhook", "--addr", c.WebhookAddr}
	args := []string{
		"--config", "model_instructions_file=" + strconv.Quote(c.agentsMdPath()),
	}
	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
		args = append(args, "--config", "hooks."+event+"="+inlineHookConfig(c.MechaBinary, hookArgs))
	}
	return args
}

func inlineHookConfig(command string, args []string) string {
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		quotedArgs[i] = strconv.Quote(arg)
	}
	return "[{hooks=[{command=" + strconv.Quote(command) + ",args=[" + strings.Join(quotedArgs, ",") + "]}]}]"
}
