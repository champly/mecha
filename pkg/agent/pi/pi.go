// Package pi implements the Pi coding agent type for mecha.
//
// Pi (pi.dev) has no built-in permission system, so no skip-permissions flag
// is needed.
package pi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

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

func (p *Pi) extensionPath() string {
	return filepath.Join(p.RoleDir, "mecha-hooks.ts")
}

// Prepare creates the Pi role directory with its prompt and hook extension.
func (p *Pi) Prepare() error {
	if err := p.PrepareRoleFile("PI.md"); err != nil {
		return err
	}
	if err := os.WriteFile(p.extensionPath(), []byte(p.extensionSource()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p.extensionPath(), err)
	}
	return nil
}

func (p *Pi) extensionSource() string {
	webhook := "http://" + p.WebhookAddr + "/webhook"
	return fmt.Sprintf(`const webhook = %s;

async function post(payload) {
  try {
    await fetch(webhook, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
  } catch {
    // agentd may already be shutting down.
  }
}

export default function (pi) {
  let lastAssistant = "";

  pi.on("agent_start", () => {
    lastAssistant = "";
  });

  pi.on("message_end", (event) => {
    if (event.message?.role !== "assistant") return;
    const content = Array.isArray(event.message.content) ? event.message.content : [];
    lastAssistant = content
      .filter((part) => part?.type === "text")
      .map((part) => part.text)
      .join("\n");
  });

  pi.on("session_start", async (_event, ctx) => {
    await post({
      hook_event_name: "SessionStart",
      session_id: ctx.sessionManager.getSessionId(),
    });
  });

  pi.on("agent_settled", async (_event, ctx) => {
    await post({
      hook_event_name: "Stop",
      session_id: ctx.sessionManager.getSessionId(),
      last_assistant_message: lastAssistant,
    });
  });
}
`, strconv.Quote(webhook))
}

// Cmd builds the *exec.Cmd for launching the Pi agent.
//
// Pi keeps the project as its working directory. The generated extension
// supplies lifecycle hooks without overriding the user's Pi agent directory.
func (p *Pi) Cmd() *exec.Cmd {
	cmd := p.NewAgentCmd(piBinary, nil, nil)
	cmd.Args = append(cmd.Args,
		"--append-system-prompt", p.piMdPath(),
		"--extension", p.extensionPath(),
	)
	return cmd
}
