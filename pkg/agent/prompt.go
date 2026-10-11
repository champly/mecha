package agent

import (
	"bytes"
	"log/slog"
	"strings"
	"text/template"

	"github.com/champly/mecha/pkg/config"
)

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
		if idx := strings.IndexAny(s, "\n。"); idx >= 0 {
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
