package agent

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"text/template"

	"github.com/champly/mecha/pkg/agent/claude"
	"github.com/champly/mecha/pkg/agent/codex"
	"github.com/champly/mecha/pkg/agent/gemini"
	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
)

var registry = map[string]types.Factory{}

func init() {
	Register("claude", claude.New)
	Register("codex", codex.New)
	Register("gemini", gemini.New)

	config.ValidateAgentType = func(typ string) bool {
		_, ok := registry[typ]
		return ok
	}
}

// Register registers an agent type factory.
func Register(name string, factory types.Factory) {
	registry[name] = factory
}

// NewFromConfig creates an Agent from pre-resolved configuration.
// Used by agentd when receiving config from Core over gRPC.
func NewFromConfig(workspace, prompt, roleName, webhookAddr string, agentCfg config.AgentConfig, mechaBinary string) (types.Agent, error) {
	factory, ok := registry[agentCfg.Type]
	if !ok {
		return nil, fmt.Errorf("agent: unknown agent type %q", agentCfg.Type)
	}

	ctx := types.AgentContext{
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

const promptTemplate = `<your_assigned_role>
{{.Role.Prompt -}}
</your_assigned_role>

<working_directory>
IMPORTANT: You were started in this directory to receive the above role assignment. The actual project you should be working on is located at:
{{.Workspace}}
</working_directory>
{{if .Role.IsCoordinator -}}

<available_roles>
You can delegate tasks by running:
	{{.MechaBinary}} ask --addr {{.Addr}} <role> "<task>"

Available roles:
{{range .OtherRoles -}}
- {{.Name}}: {{firstLine .Prompt}}
{{end -}}
</available_roles>
{{end}}`

type promptData struct {
	Workspace   string
	MechaBinary string
	Addr        string
	Role        config.Role
	OtherRoles  []config.Role
}

var tmpl = template.Must(template.New("prompt").Funcs(template.FuncMap{
	"firstLine": func(s string) string {
		if idx := strings.IndexAny(s, "\n。"); idx > 0 {
			return s[:idx]
		}
		return s
	},
}).Parse(promptTemplate))

// RenderPrompt renders the role prompt template for the given role.
func RenderPrompt(workspace string, runtime config.Runtime, role config.Role, allRoles []config.Role) string {
	otherRoles := make([]config.Role, 0, len(allRoles))
	for _, r := range allRoles {
		if r.Name != role.Name {
			otherRoles = append(otherRoles, r)
		}
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{
		Workspace:   workspace,
		MechaBinary: runtime.MechaBinary,
		Addr:        runtime.Addr,
		Role:        role,
		OtherRoles:  otherRoles,
	}); err != nil {
		slog.Warn("prompt template render failed, falling back to raw prompt", "role", role.Name, "err", err)
		return role.Prompt
	}
	return buf.String()
}
