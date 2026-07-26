package config

import (
	"reflect"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.Agent != "claude-sonnet-4-6" {
		t.Errorf("Agent = %q, want %q", cfg.Agent, "claude-sonnet-4-6")
	}
	if cfg.Profile != "softwarecompany" {
		t.Errorf("Profile = %q, want %q", cfg.Profile, "softwarecompany")
	}
	if len(cfg.Agents) == 0 {
		t.Error("expected at least one agent")
	}
	if len(cfg.Profiles) == 0 {
		t.Error("expected at least one profile")
	}
}

func TestValidateInvalidAgentType(t *testing.T) {
	// Save and restore the global ValidateAgentType hook.
	saved := ValidateAgentType
	t.Cleanup(func() { ValidateAgentType = saved })

	ValidateAgentType = func(typ string) bool {
		valid := map[string]bool{
			"claude": true,
			"codex":  true,
			"gemini": true,
		}
		return valid[typ]
	}

	cfg := Config{
		Agents: []AgentConfig{
			{Name: "good", Type: "claude"},
			{Name: "bad", Type: "unknown-type"},
		},
	}

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected error for unknown agent type, got nil")
	}
	if err.Error() != `config: unknown agent type "unknown-type"` {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateProfileRequired(t *testing.T) {
	cfg := Config{
		Agents: []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p1": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}}}},
		},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for empty profile, got nil")
	}
}

func TestValidateProfileNotFound(t *testing.T) {
	cfg := Config{
		Profile: "missing",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p1": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}}}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
	if err.Error() != `config: profile "missing" not found in profiles` {
		t.Errorf("unexpected error message: %v", err)
	}
}

// Role-level params/envs merge over the agent-level ones (role wins per key)
// instead of replacing them wholesale.
func TestCompleteMergesRoleParamsAndEnvs(t *testing.T) {
	cfg := Config{
		Agent:   "base",
		Profile: "p",
		Agents: []AgentConfig{{
			Name:   "base",
			Type:   "claude",
			Params: map[string]any{"a": 1, "b": 2},
			Envs:   map[string]string{"X": "1", "Y": "2"},
		}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{
				Name:          "r",
				IsCoordinator: true,
				Agent: AgentConfig{
					Name:   "base",
					Params: map[string]any{"b": 20, "c": 30},
					Envs:   map[string]string{"Y": "20", "Z": "30"},
				},
			}}},
		},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg.complete()

	role := cfg.Profiles["p"].Roles[0]
	wantParams := map[string]any{"a": 1, "b": 20, "c": 30}
	if !reflect.DeepEqual(role.Agent.Params, wantParams) {
		t.Errorf("Params = %v, want %v", role.Agent.Params, wantParams)
	}
	wantEnvs := map[string]string{"X": "1", "Y": "20", "Z": "30"}
	if !reflect.DeepEqual(role.Agent.Envs, wantEnvs) {
		t.Errorf("Envs = %v, want %v", role.Agent.Envs, wantEnvs)
	}

	// The agent-level defaults must not be mutated by the merge.
	wantBase := map[string]any{"a": 1, "b": 2}
	if !reflect.DeepEqual(cfg.Agents[0].Params, wantBase) {
		t.Errorf("base Params = %v, want %v", cfg.Agents[0].Params, wantBase)
	}
}
