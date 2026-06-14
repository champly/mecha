package config

import (
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var defaultConfigYAML []byte

const (
	configDirName  = ".mecha"
	configFileName = "config.yaml"
	rolesDirName   = "roles"
)

// RoleDir returns the project-local role directory path.
func RoleDir(workspace, roleName string) string {
	return filepath.Join(workspace, configDirName, rolesDirName, roleName)
}

// MechaBinary is the path to the mecha binary used for webhook callbacks.
// It defaults to "mecha" (resolved from PATH). Override at build time or
// set programmatically before creating agents.
var MechaBinary = "mecha"

// WebhookPort is the port the HTTP server listens on for webhook callbacks.
// It is set by [core.Core] before preparing agents.
var WebhookPort string

// MechaDir returns the path to the mecha global directory (~/.mecha).
func MechaDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot determine user home directory: %w", err)
	}
	return filepath.Join(home, configDirName), nil
}

// DefaultConfigPath returns the default config file path (~/.mecha/config.yaml).
func DefaultConfigPath() (string, error) {
	dir, err := MechaDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

type AgentConfig struct {
	Name   string            `yaml:"name,omitempty"`
	Type   string            `yaml:"type"`
	Model  string            `yaml:"model"`
	Params map[string]any    `yaml:"params"`
	Envs   map[string]string `yaml:"envs"`
}

type Role struct {
	Name          string `yaml:"name"`
	Prompt        string `yaml:"prompt"`
	IsCoordinator bool   `yaml:"is_coordinator,omitempty"`

	Agent AgentConfig `yaml:"agent"`
}

type ProfileConfig struct {
	Roles []Role `yaml:"roles"`
}

type Config struct {
	Agent  string        `yaml:"agent"`
	Agents []AgentConfig `yaml:"agents"`

	Profile  string                   `yaml:"profile"`
	Profiles map[string]ProfileConfig `yaml:"profiles"`
}

// LoadConfig reads YAML config from path, validates it, and completes it with defaults.
// If path is empty, ~/.mecha/config.yaml is used.
func LoadConfig(path string) (Config, error) {
	c, err := parseConfigFile(path)
	if err != nil {
		return Config{}, err
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}

	c.complete()
	return c, nil
}

func parseConfigFile(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		p, err := DefaultConfigPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("config: file not found %q", path)
		}
		return Config{}, err
	}

	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validate checks basic config consistency:
// - agents[].name must be non-empty and unique
// - config.agent must reference an existing agent when set
// - each role must resolve to an existing agent name
// - each profile must have exactly one coordinator role (is_coordinator=true)
func (c Config) validate() error {
	agentNames := make(map[string]struct{}, len(c.Agents))
	for _, agent := range c.Agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			return fmt.Errorf("config: agent name is required")
		}
		if _, exists := agentNames[name]; exists {
			return fmt.Errorf("config: duplicate agent name %q", name)
		}
		agentNames[name] = struct{}{}
	}

	defaultAgent := strings.TrimSpace(c.Agent)
	if defaultAgent != "" {
		if _, ok := agentNames[defaultAgent]; !ok {
			return fmt.Errorf("config: default agent %q not found", defaultAgent)
		}
	}

	for profileName, profile := range c.Profiles {
		coordinatorCount := 0
		for _, role := range profile.Roles {
			if role.IsCoordinator {
				coordinatorCount++
			}

			name := strings.TrimSpace(role.Agent.Name)
			if name == "" {
				name = defaultAgent
			}
			if name == "" {
				return fmt.Errorf("config: role %q in profile %q has no agent name and config.agent is empty", role.Name, profileName)
			}
			if _, ok := agentNames[name]; !ok {
				return fmt.Errorf("config: role %q in profile %q references unknown agent %q", role.Name, profileName, name)
			}
		}

		if coordinatorCount == 0 {
			return fmt.Errorf("config: profile %q must have one role with is_coordinator=true", profileName)
		}
		if coordinatorCount > 1 {
			return fmt.Errorf("config: profile %q has multiple coordinator roles (is_coordinator=true)", profileName)
		}
	}

	return nil
}

// complete normalizes and enriches config for later usage.
func (c *Config) complete() {
	c.Agent = strings.TrimSpace(c.Agent)
	c.Profile = strings.TrimSpace(c.Profile)

	for i := range c.Agents {
		c.Agents[i].Name = strings.TrimSpace(c.Agents[i].Name)
		c.Agents[i].Type = strings.TrimSpace(c.Agents[i].Type)
		c.Agents[i].Model = strings.TrimSpace(c.Agents[i].Model)
		c.Agents[i].Params = cloneParams(c.Agents[i].Params)
	}

	for profileName, profile := range c.Profiles {
		for i := range profile.Roles {
			role := &profile.Roles[i]
			role.Name = strings.TrimSpace(role.Name)
			role.Prompt = strings.TrimSpace(role.Prompt)

			agentName := strings.TrimSpace(role.Agent.Name)
			if agentName == "" {
				agentName = c.Agent
			}

			base, ok := c.findAgent(agentName)
			if !ok {
				continue
			}

			resolved := AgentConfig{
				Name:   agentName,
				Type:   base.Type,
				Model:  base.Model,
				Params: cloneParams(base.Params),
				Envs:   maps.Clone(base.Envs),
			}

			if v := strings.TrimSpace(role.Agent.Type); v != "" {
				resolved.Type = v
			}
			if v := strings.TrimSpace(role.Agent.Model); v != "" {
				resolved.Model = v
			}
			if role.Agent.Params != nil {
				resolved.Params = cloneParams(role.Agent.Params)
			}
			if role.Agent.Envs != nil {
				resolved.Envs = maps.Clone(role.Agent.Envs)
			}

			role.Agent = resolved
		}
		c.Profiles[profileName] = profile
	}
}

func (c Config) findAgent(name string) (AgentConfig, bool) {
	for _, agent := range c.Agents {
		if strings.TrimSpace(agent.Name) == name {
			return agent, true
		}
	}
	return AgentConfig{}, false
}

// InitConfig writes the default config.yaml to ~/.mecha/config.yaml.
// If the file already exists and force is false, the existing file is renamed
// to config.yaml.bak before writing the new one. If force is true, it is
// overwritten directly.
// The directory ~/.mecha/ is created if it does not exist.
func InitConfig(force bool) (path string, err error) {
	dir, err := MechaDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("config: cannot create directory %q: %w", dir, err)
	}

	path = filepath.Join(dir, configFileName)

	if _, statErr := os.Stat(path); statErr == nil {
		if force {
			// Overwrite directly
			if err := os.WriteFile(path, defaultConfigYAML, 0644); err != nil {
				return "", fmt.Errorf("config: cannot write %q: %w", path, err)
			}
			return path, nil
		}

		// Backup existing file
		bakPath := path + ".bak"
		os.Remove(bakPath) // remove old bak if any
		if err := os.Rename(path, bakPath); err != nil {
			return "", fmt.Errorf("config: cannot rename %q to %q: %w", path, bakPath, err)
		}
	}

	if err := os.WriteFile(path, defaultConfigYAML, 0644); err != nil {
		return "", fmt.Errorf("config: cannot write %q: %w", path, err)
	}

	return path, nil
}

func cloneParams(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
