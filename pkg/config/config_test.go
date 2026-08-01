package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestValidateDuplicateRoleName(t *testing.T) {
	cfg := Config{
		Profile: "p1",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p1": {Roles: []Role{
				{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}},
				{Name: "r", Agent: AgentConfig{Name: "a"}},
			}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("expected error for duplicate role name, got nil")
	}
	if err.Error() != `config: duplicate role name "r" in profile "p1"` {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateEmptyRoleName(t *testing.T) {
	cfg := Config{
		Profile: "p1",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p1": {Roles: []Role{
				{Name: " ", IsCoordinator: true, Agent: AgentConfig{Name: "a"}},
			}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("expected error for empty role name, got nil")
	}
	if err.Error() != `config: profile "p1" has a role with empty name` {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateAgentNameRequired(t *testing.T) {
	cfg := Config{
		Profile: "p",
		Agents:  []AgentConfig{{Name: " "}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}}}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != "config: agent name is required" {
		t.Errorf("validate() = %v, want agent-name-required error", err)
	}
}

func TestValidateDuplicateAgentName(t *testing.T) {
	cfg := Config{
		Profile: "p",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}, {Name: "a", Type: "codex"}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}}}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != `config: duplicate agent name "a"` {
		t.Errorf("validate() = %v, want duplicate-agent-name error", err)
	}
}

func TestValidateDefaultAgentNotFound(t *testing.T) {
	cfg := Config{
		Agent:   "missing",
		Profile: "p",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "a"}}}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != `config: default agent "missing" not found` {
		t.Errorf("validate() = %v, want default-agent-not-found error", err)
	}
}

func TestValidateRoleReferencesUnknownAgent(t *testing.T) {
	cfg := Config{
		Profile: "p",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{Name: "r", IsCoordinator: true, Agent: AgentConfig{Name: "ghost"}}}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != `config: role "r" in profile "p" references unknown agent "ghost"` {
		t.Errorf("validate() = %v, want unknown-agent-reference error", err)
	}
}

func TestValidateCoordinatorMissing(t *testing.T) {
	cfg := Config{
		Profile: "p",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{{Name: "r", Agent: AgentConfig{Name: "a"}}}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != `config: profile "p" must have one role with is_coordinator=true` {
		t.Errorf("validate() = %v, want coordinator-missing error", err)
	}
}

func TestValidateCoordinatorMultiple(t *testing.T) {
	cfg := Config{
		Profile: "p",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p": {Roles: []Role{
				{Name: "r1", IsCoordinator: true, Agent: AgentConfig{Name: "a"}},
				{Name: "r2", IsCoordinator: true, Agent: AgentConfig{Name: "a"}},
			}},
		},
	}
	err := cfg.validate()
	if err == nil || err.Error() != `config: profile "p" has multiple coordinator roles (is_coordinator=true)` {
		t.Errorf("validate() = %v, want multiple-coordinator error", err)
	}
}

func TestParseConfigFileErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("file not found", func(t *testing.T) {
		_, err := parseConfigFile(filepath.Join(dir, "nope.yaml"))
		if err == nil || err.Error() != `config: file not found "`+filepath.Join(dir, "nope.yaml")+`"` {
			t.Errorf("parseConfigFile() = %v, want file-not-found error", err)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		path := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(path, []byte("agents: [unclosed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := parseConfigFile(path); err == nil {
			t.Error("expected yaml parse error, got nil")
		}
	})
}

func TestInitConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".mecha", "config.yaml")

	if _, err := InitConfig(false); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.yaml not created: %v", err)
	}

	// Existing file is backed up to .bak when force is false.
	original, _ := os.ReadFile(path)
	os.WriteFile(path, []byte("custom"), 0o644)
	if _, err := InitConfig(false); err != nil {
		t.Fatalf("InitConfig backup: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != "custom" {
		t.Errorf("backup = %q, %v; want previous content", bak, err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(original) {
		t.Errorf("config.yaml not restored to defaults after backup")
	}

	// force overwrites without a backup.
	os.WriteFile(path, []byte("custom"), 0o644)
	if _, err := InitConfig(true); err != nil {
		t.Fatalf("InitConfig force: %v", err)
	}
	current, _ = os.ReadFile(path)
	if string(current) != string(original) {
		t.Errorf("force did not overwrite with defaults")
	}
}

func TestValidateRoleLevelAgentTypeOverride(t *testing.T) {
	saved := ValidateAgentType
	t.Cleanup(func() { ValidateAgentType = saved })

	ValidateAgentType = func(typ string) bool {
		return typ == "claude" || typ == "codex"
	}

	cfg := Config{
		Profile: "p1",
		Agents:  []AgentConfig{{Name: "a", Type: "claude"}},
		Profiles: map[string]ProfileConfig{
			"p1": {Roles: []Role{{
				Name:          "r",
				IsCoordinator: true,
				Agent:         AgentConfig{Name: "a", Type: "unknown-type"},
			}}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("expected error for role-level unknown agent type, got nil")
	}
	if err.Error() != `config: role "r" in profile "p1": unknown agent type "unknown-type"` {
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

func TestNewFileLogger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logger, f, err := NewFileLogger("/Users/test/project")
	if err != nil {
		t.Fatalf("NewFileLogger() error: %v", err)
	}
	defer f.Close()

	wantPath := filepath.Join(home, ".mecha", "logs", "Users_test_project", time.Now().Format(time.DateOnly)+".log")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("log file %q not created: %v", wantPath, err)
	}

	logger.Info("hello", "key", "value")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), `hello`) || !strings.Contains(string(data), `key=value`) {
		t.Errorf("log file missing expected content, got: %s", data)
	}
}
