package config

import "testing"

func TestLoadConfig(t *testing.T) {
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.Agent != "deepseek-v4-flash" {
		t.Errorf("Agent = %q, want %q", cfg.Agent, "deepseek-v4-flash")
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
