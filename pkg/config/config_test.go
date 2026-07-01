package config

import "testing"

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
