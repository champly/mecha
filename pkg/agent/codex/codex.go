// Package codex implements the Codex agent type for mecha.
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

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
	agentID     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookPort string
}

// New returns a Codex agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Codex{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		agentID:     ctx.AgentID,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookPort: runtime.WebhookPort,
	}, nil
}

// ID returns the agent's unique identifier.
func (c *Codex) ID() string {
	return c.agentID
}

// agentsMdPath returns the path to the agent's AGENTS.md file.
func (c *Codex) agentsMdPath() string {
	return filepath.Join(c.roleDir, "AGENTS.md")
}

// configDir returns the path to the agent's .codex directory.
func (c *Codex) configDir() string {
	return filepath.Join(c.roleDir, ".codex")
}

// configTomlPath returns the path to the agent's config.toml file.
func (c *Codex) configTomlPath() string {
	return filepath.Join(c.roleDir, ".codex", "config.toml")
}

// Prepare creates the full Codex role directory.
func (c *Codex) Prepare() error {
	if err := c.writePrompt(); err != nil {
		return err
	}
	return c.writeConfig()
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

const configTomlTemplate = `[hooks]

[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
command = "{{.MechaBinary}}"
args = [{{range $i, $a := .HookArgs}}{{if $i}}, {{end}}"{{$a}}"{{end}}]

[[hooks.Stop]]
[[hooks.Stop.hooks]]
command = "{{.MechaBinary}}"
args = [{{range $i, $a := .HookArgs}}{{if $i}}, {{end}}"{{$a}}"{{end}}]
`

type configTomlData struct {
	MechaBinary string
	HookArgs    []string
}

var configTmpl = template.Must(template.New("config").Parse(configTomlTemplate))

func (c *Codex) writeConfig() error {
	configDir := c.configDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("codex: create .codex dir: %w", err)
	}

	f, err := os.Create(c.configTomlPath())
	if err != nil {
		return fmt.Errorf("codex: create config.toml: %w", err)
	}
	defer f.Close()

	if err := configTmpl.Execute(f, configTomlData{
		MechaBinary: c.mechaBinary,
		HookArgs:    []string{"webhook", "--id", c.agentID, "--port", c.webhookPort},
	}); err != nil {
		return fmt.Errorf("codex: render config.toml: %w", err)
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
	args = append(args, "--cd", c.roleDir)

	binary := c.cfg.Binary
	if binary == "" {
		binary = codexBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.roleDir
	for k, v := range c.cfg.Envs {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}
