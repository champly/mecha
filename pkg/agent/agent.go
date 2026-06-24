package agent

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"text/template"

	"github.com/champly/mecha/pkg/agent/claude"
	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
	"github.com/google/uuid"
)

var registry = map[string]types.Factory{}

func init() {
	Register("claude", claude.New)
}

// Register registers an agent type factory.
func Register(name string, factory types.Factory) {
	registry[name] = factory
}

// New creates an Agent for the given role within the workspace.
func New(workspace string, roleName string, cfg config.Config, runtime config.Runtime) (types.Agent, error) {
	profile, ok := cfg.Profiles[cfg.Profile]
	if !ok {
		return nil, fmt.Errorf("agent: unknown profile %q", cfg.Profile)
	}

	var role *config.Role
	for i := range profile.Roles {
		if profile.Roles[i].Name == roleName {
			role = &profile.Roles[i]
			break
		}
	}
	if role == nil {
		return nil, fmt.Errorf("agent: role %q not found in profile %q", roleName, cfg.Profile)
	}

	factory, ok := registry[role.Agent.Type]
	if !ok {
		return nil, fmt.Errorf("agent: unknown agent type %q", role.Agent.Type)
	}

	ctx := types.AgentContext{
		Workspace: workspace,
		RoleDir:   config.RoleDir(workspace, roleName),
		Prompt:    renderPrompt(runtime, *role, profile.Roles),
		AgentID:   uuid.NewString(),
	}
	return factory(ctx, role.Agent, runtime)
}

const promptTemplate = `<your_assigned_role>
{{.Role.Prompt -}}
</your_assigned_role>
{{if .Role.IsCoordinator -}}

<available_roles>
You can delegate tasks by running:
  {{.MechaBinary}} ask --port {{.WebhookPort}} <role> "<task>"

Available roles:
{{range .OtherRoles -}}
- {{.Name}}: {{firstLine .Prompt}}
{{end -}}
</available_roles>
{{end}}`

type promptData struct {
	MechaBinary string
	WebhookPort string
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

func renderPrompt(runtime config.Runtime, role config.Role, allRoles []config.Role) string {
	otherRoles := make([]config.Role, 0, len(allRoles))
	for _, r := range allRoles {
		if r.Name != role.Name {
			otherRoles = append(otherRoles, r)
		}
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{
		MechaBinary: runtime.MechaBinary,
		WebhookPort: runtime.WebhookPort,
		Role:        role,
		OtherRoles:  otherRoles,
	}); err != nil {
		slog.Warn("prompt template render failed, falling back to raw prompt", "role", role.Name, "err", err)
		return role.Prompt
	}
	return buf.String()
}
