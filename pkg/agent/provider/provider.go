// Package provider holds the contract and shared helpers that every agent type
// (claude, codex, gemini, codebuddy, pi) builds on.
package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/champly/mecha/pkg/config"
)

// AgentContext bundles the runtime environment for an agent instance.
type AgentContext struct {
	Workspace   string // project root (cmd.Dir)
	RoleDir     string // agent-specific files (CLAUDE.md, settings.json)
	Prompt      string // role instruction (injected via --append-system-prompt-file)
	WebhookAddr string // agentd address to POST hook events to
}

// Factory creates an Agent from the given parameters.
type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)

// Agent is the interface all agent types must implement.
type Agent interface {
	Prepare() error
	Cmd() *exec.Cmd
	ParseHookEvent(raw []byte) (HookEvent, error)
}

// Base carries the runtime fields shared by all agent providers.
type Base struct {
	Workspace   string
	RoleDir     string
	Prompt      string
	cfg         config.AgentConfig
	MechaBinary string
	WebhookAddr string
}

// NewBase populates a Base from the factory inputs.
func NewBase(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) Base {
	return Base{
		Workspace:   ctx.Workspace,
		RoleDir:     ctx.RoleDir,
		Prompt:      ctx.Prompt,
		cfg:         cfg,
		MechaBinary: runtime.MechaBinary,
		WebhookAddr: ctx.WebhookAddr,
	}
}

// PrepareRoleFile creates the role dir and writes the role prompt to filename.
func (b *Base) PrepareRoleFile(filename string) error {
	if err := os.MkdirAll(b.RoleDir, 0o755); err != nil {
		return fmt.Errorf("create dir %q: %w", b.RoleDir, err)
	}
	if err := os.WriteFile(filepath.Join(b.RoleDir, filename), []byte(b.Prompt), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return nil
}

// ResolveBinary resolves the agent binary (cfg.Binary or defaultBinary) and
// verifies it exists, failing at factory time instead of at PTY launch.
func (b *Base) ResolveBinary(defaultBinary string) (string, error) {
	binary := b.cfg.Binary
	if binary == "" {
		binary = defaultBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return "", fmt.Errorf("agent binary %q: %w", binary, err)
	}
	return binary, nil
}

// NewAgentCmd builds the process command with the model flag, merged
// params/envs, binary fallback, and workspace directory. Providers append
// their own flags and may override cmd.Dir.
func (b *Base) NewAgentCmd(defaultBinary string, defaultParams map[string]any, defaultEnvs map[string]string) *exec.Cmd {
	args := make([]string, 0, 8)
	if b.cfg.Model != "" {
		args = append(args, "--model", b.cfg.Model)
	}
	args = append(args, BuildArgs(b.cfg.Params, defaultParams)...)

	binary := b.cfg.Binary
	if binary == "" {
		binary = defaultBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = b.Workspace
	cmd.Env = BuildEnv(b.cfg.Envs, defaultEnvs)
	return cmd
}
