package agent

import (
	"strings"
	"testing"

	"github.com/champly/mecha/pkg/config"
)

func TestRenderPrompt_Coordinator(t *testing.T) {
	role := config.Role{
		Name:          "coordinator",
		Prompt:        "You are the project coordinator.",
		IsCoordinator: true,
	}
	allRoles := []config.Role{
		{Name: "coordinator", IsCoordinator: true, Prompt: "Coordinator."},
		{Name: "reviewer", Prompt: "Reviews code."},
		{Name: "implementer", Prompt: "Writes code."},
	}

	runtime := config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
	content := RenderPrompt("/Users/me/myproject", runtime, role, allRoles)

	if !strings.Contains(content, "<your_assigned_role>") {
		t.Errorf("missing <your_assigned_role>")
	}
	if !strings.Contains(content, "<working_directory>") {
		t.Errorf("missing <working_directory>")
	}
	if !strings.Contains(content, "/Users/me/myproject") {
		t.Errorf("missing workspace path")
	}
	if !strings.Contains(content, "<available_roles>") {
		t.Errorf("coordinator should have <available_roles>")
	}
	if !strings.Contains(content, "reviewer") {
		t.Errorf("should list reviewer")
	}
	if !strings.Contains(content, "mecha ask") {
		t.Errorf("should mention mecha ask")
	}
	if !strings.Contains(content, "You are the project coordinator.") {
		t.Errorf("missing original prompt")
	}
}

func TestRenderPrompt_Specialist(t *testing.T) {
	role := config.Role{
		Name:   "reviewer",
		Prompt: "You are a code reviewer.",
	}

	runtime := config.Runtime{MechaBinary: "mecha", Addr: "127.0.0.1:12345"}
	content := RenderPrompt("/Users/me/myproject", runtime, role, nil)

	if !strings.Contains(content, "<your_assigned_role>") {
		t.Errorf("missing <your_assigned_role>")
	}
	if !strings.Contains(content, "<working_directory>") {
		t.Errorf("missing <working_directory>")
	}
	if !strings.Contains(content, "/Users/me/myproject") {
		t.Errorf("missing workspace path")
	}
	if strings.Contains(content, "<available_roles>") {
		t.Errorf("specialist should not have <available_roles>")
	}
	if !strings.Contains(content, "You are a code reviewer.") {
		t.Errorf("missing original prompt")
	}
}
