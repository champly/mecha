// Package pi implements the Pi coding agent type for mecha.
//
// Pi is an open-source terminal-native coding agent (pi.dev). Unlike Claude
// Code, Pi has no built-in permission system, so no --dangerously-skip-permissions
// or -y flag is needed. Pi discovers .pi/settings.json from its working
// directory and walks parent directories for AGENTS.md/CLAUDE.md context files.
package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term/driver"
)

const piBinary = "pi"

// defaultParams is empty because Pi has no permission system to bypass.
var defaultParams = map[string]any{}

// defaultEnvs is empty; Pi inherits ANTHROPIC_API_KEY etc. from the parent
// process and does not have a PI_CODE_MAX_OUTPUT_TOKENS-style variable.
var defaultEnvs = map[string]string{}

// Pi handles the Pi coding agent type for a specific role.
type Pi struct {
	workspace   string
	roleDir     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookAddr string
}

// New returns a Pi agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Pi{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookAddr: ctx.WebhookAddr,
	}, nil
}

func (p *Pi) piMdPath() string {
	return filepath.Join(p.roleDir, "PI.md")
}

func (p *Pi) piDir() string {
	return filepath.Join(p.roleDir, ".pi")
}

func (p *Pi) settingsPath() string {
	return filepath.Join(p.piDir(), "settings.json")
}

// Prepare creates the Pi role directory with PI.md and .pi/settings.json.
func (p *Pi) Prepare() error {
	if err := p.writePrompt(); err != nil {
		return err
	}
	return p.writeSettings()
}

func (p *Pi) writePrompt() error {
	if err := os.MkdirAll(p.roleDir, 0o755); err != nil {
		return fmt.Errorf("pi: create dir %q: %w", p.roleDir, err)
	}

	if err := os.WriteFile(p.piMdPath(), []byte(p.prompt), 0o644); err != nil {
		return fmt.Errorf("pi: write PI.md: %w", err)
	}
	return nil
}

func (p *Pi) writeSettings() error {
	piDir := p.piDir()
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		return fmt.Errorf("pi: create .pi dir: %w", err)
	}

	// Pi's hook command is a single shell command string (unlike Claude Code's
	// separate command+args array). Use shell quoting for paths with spaces.
	webhookCmd := fmt.Sprintf("%s webhook --addr %s", driver.QuoteShell(p.mechaBinary), driver.QuoteShell(p.webhookAddr))

	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
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
			"Stop": []any{
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

	f, err := os.Create(p.settingsPath())
	if err != nil {
		return fmt.Errorf("pi: create settings.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return fmt.Errorf("pi: encode settings.json: %w", err)
	}
	return nil
}

// Cmd builds the *exec.Cmd for launching the Pi agent.
//
// Pi discovers .pi/settings.json from its working directory, which is set
// to the role directory (like Gemini). The workspace is still accessible
// via parent-directory traversal for AGENTS.md/CLAUDE.md context files.
func (p *Pi) Cmd() *exec.Cmd {
	args := []string{}
	if p.cfg.Model != "" {
		args = append(args, "--model", p.cfg.Model)
	}

	args = append(args, agenttypes.BuildArgs(p.cfg.Params, defaultParams)...)
	args = append(args, "--append-system-prompt", p.prompt)

	binary := p.cfg.Binary
	if binary == "" {
		binary = piBinary
	}
	cmd := exec.Command(binary, args...)
	// Pi discovers .pi/settings.json relative to CWD.
	cmd.Dir = p.roleDir
	cmd.Env = agenttypes.BuildEnv(p.cfg.Envs, defaultEnvs)
	return cmd
}
