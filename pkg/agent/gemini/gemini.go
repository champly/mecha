// Package gemini implements the Gemini CLI agent type for mecha.
package gemini

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

const geminiBinary = "gemini"

var defaultParams = map[string]any{
	"yolo": true,
}

// Gemini handles the Gemini CLI agent type for a specific role.
type Gemini struct {
	workspace   string
	roleDir     string
	prompt      string
	cfg         config.AgentConfig
	mechaBinary string
	webhookAddr string
}

// New returns a Gemini agent helper.
func New(ctx agenttypes.AgentContext, cfg config.AgentConfig, runtime config.Runtime) (agenttypes.Agent, error) {
	return &Gemini{
		workspace:   ctx.Workspace,
		roleDir:     ctx.RoleDir,
		prompt:      ctx.Prompt,
		cfg:         cfg,
		mechaBinary: runtime.MechaBinary,
		webhookAddr: ctx.WebhookAddr,
	}, nil
}

func (g *Gemini) geminiMdPath() string {
	return filepath.Join(g.roleDir, "GEMINI.md")
}

func (g *Gemini) settingsDir() string {
	return filepath.Join(g.roleDir, ".gemini")
}

func (g *Gemini) settingsPath() string {
	return filepath.Join(g.roleDir, ".gemini", "settings.json")
}

// Prepare creates the full Gemini CLI role directory.
func (g *Gemini) Prepare() error {
	if err := g.writePrompt(); err != nil {
		return err
	}
	return g.writeSettings()
}

func (g *Gemini) writePrompt() error {
	if err := os.MkdirAll(g.roleDir, 0o755); err != nil {
		return fmt.Errorf("gemini: create dir %q: %w", g.roleDir, err)
	}

	if err := os.WriteFile(g.geminiMdPath(), []byte(g.prompt), 0o644); err != nil {
		return fmt.Errorf("gemini: write GEMINI.md: %w", err)
	}
	return nil
}

func (g *Gemini) writeSettings() error {
	settingsDir := g.settingsDir()
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("gemini: create .gemini dir: %w", err)
	}

	webhookCmd := fmt.Sprintf("%s webhook --addr %s", g.mechaBinary, g.webhookAddr)

	settings := map[string]any{
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
		},
	}

	f, err := os.Create(g.settingsPath())
	if err != nil {
		return fmt.Errorf("gemini: create settings.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return fmt.Errorf("gemini: encode settings.json: %w", err)
	}
	return nil
}

// Cmd builds the *exec.Cmd for launching the Gemini CLI agent.
func (g *Gemini) Cmd() *exec.Cmd {
	args := []string{}
	if g.cfg.Model != "" {
		args = append(args, "--model", g.cfg.Model)
	}
	args = append(args, agenttypes.BuildArgs(g.cfg.Params, defaultParams)...)

	binary := g.cfg.Binary
	if binary == "" {
		binary = geminiBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = g.roleDir
	cmd.Env = agenttypes.BuildEnv(g.cfg.Envs, nil)
	return cmd
}
