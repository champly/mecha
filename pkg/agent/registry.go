package agent

import (
	"fmt"

	"github.com/champly/mecha/pkg/agent/claude"
	"github.com/champly/mecha/pkg/agent/codebuddy"
	"github.com/champly/mecha/pkg/agent/codex"
	"github.com/champly/mecha/pkg/agent/gemini"
	"github.com/champly/mecha/pkg/agent/pi"
	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/config"
)

var registry = map[string]agentprovider.Factory{}

func init() {
	Register("claude", claude.New)
	Register("codebuddy", codebuddy.New)
	Register("codex", codex.New)
	Register("gemini", gemini.New)
	Register("pi", pi.New)
}

// ValidateAgentType reports whether typ is a registered agent type.
func ValidateAgentType(typ string) bool {
	_, ok := registry[typ]
	return ok
}

// Register registers an agent type factory.
func Register(name string, factory agentprovider.Factory) {
	registry[name] = factory
}

// NewFromConfig creates an Agent from pre-resolved configuration.
func NewFromConfig(workspace, prompt, roleName, webhookAddr string, agentCfg config.AgentConfig, mechaBinary string) (agentprovider.Agent, error) {
	factory, ok := registry[agentCfg.Type]
	if !ok {
		return nil, fmt.Errorf("agent: unknown agent type %q", agentCfg.Type)
	}

	ctx := agentprovider.AgentContext{
		Workspace:   workspace,
		RoleDir:     config.RoleDir(workspace, roleName),
		Prompt:      prompt,
		WebhookAddr: webhookAddr,
	}

	runtime := config.Runtime{
		MechaBinary: mechaBinary,
	}

	return factory(ctx, agentCfg, runtime)
}
