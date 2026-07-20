// Package codex implements the Codex agent type for mecha.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const codexBinary = "codex"

var defaultParams = map[string]any{
	"dangerously-bypass-approvals-and-sandbox": true,
}

// Codex handles the Codex CLI agent type for a specific role.
type Codex struct {
	workspace   string
	roleDir     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookAddr string
}

// New returns a Codex agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Codex{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookAddr: ctx.WebhookAddr,
	}, nil
}

func (c *Codex) agentsMdPath() string {
	return filepath.Join(c.roleDir, "AGENTS.md")
}

// Prepare creates the role-specific instructions file consumed by Codex.
func (c *Codex) Prepare() error {
	return c.writePrompt()
}

func (c *Codex) writePrompt() error {
	if err := os.MkdirAll(c.roleDir, 0o755); err != nil {
		return fmt.Errorf("codex: create dir %q: %w", c.roleDir, err)
	}

	if err := os.WriteFile(c.agentsMdPath(), []byte(c.prompt), 0o644); err != nil {
		return fmt.Errorf("codex: write AGENTS.md: %w", err)
	}
	return nil
}

// Cmd builds the *exec.Cmd for launching the Codex agent.
func (c *Codex) Cmd() *exec.Cmd {
	args := []string{}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}

	args = append(args, agenttypes.BuildArgs(c.cfg.Params, defaultParams)...)
	args = append(args, c.configArgs()...)
	args = append(args, "--cd", c.workspace)

	binary := c.cfg.Binary
	if binary == "" {
		binary = codexBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.workspace
	cmd.Env = agenttypes.BuildEnv(c.cfg.Envs, nil)
	return cmd
}

func (c *Codex) configArgs() []string {
	hookArgs := []string{"webhook", "--addr", c.webhookAddr}
	args := []string{
		"--config", "model_instructions_file=" + quoteTOMLString(c.agentsMdPath()),
	}
	for _, event := range []string{agenttypes.EventSessionStart, agenttypes.EventStop, agenttypes.EventStopFailure} {
		args = append(args, "--config", "hooks."+event+"="+inlineHookConfig(c.mechaBinary, hookArgs))
	}
	return args
}

func inlineHookConfig(command string, args []string) string {
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		quotedArgs[i] = quoteTOMLString(arg)
	}
	return "[{hooks=[{command=" + quoteTOMLString(command) + ",args=[" + joinComma(quotedArgs) + "]}]}]"
}

func quoteTOMLString(value string) string {
	return strconv.Quote(value)
}

func joinComma(values []string) string {
	if len(values) == 0 {
		return ""
	}
	joined := values[0]
	for _, value := range values[1:] {
		joined += "," + value
	}
	return joined
}
