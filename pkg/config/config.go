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

// ValidateAgentType is an optional hook for validating agent type strings.
// When nil, validation is skipped. The agent package sets this during init().
var ValidateAgentType func(typ string) bool

// MechaBinary is the default mecha binary path for webhook callbacks; Core
// copies it into Runtime.MechaBinary at startup. Override via ldflags:
//
//	-X github.com/champly/mecha/pkg/config.MechaBinary=/custom/path
var MechaBinary = "mecha"

// Runtime holds values that are determined at startup and needed throughout
// the agent lifecycle. It is passed explicitly to avoid hidden coupling
// between core, agent, and provider packages.
type Runtime struct {
	MechaBinary string // path to mecha binary (from config.MechaBinary by default)
	Addr        string // Core gRPC listen address (host:port)
}

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
	Binary string            `yaml:"binary,omitempty"`
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

// validate checks basic consistency: unique agent names, resolvable agent
// references, and exactly one coordinator role per profile.
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

		if ValidateAgentType != nil {
			agentType := strings.TrimSpace(agent.Type)
			if !ValidateAgentType(agentType) {
				return fmt.Errorf("config: unknown agent type %q", agentType)
			}
		}
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

// complete normalizes fields and resolves each role's agent config in place.
// Must be called once, immediately after validate, before concurrent use.
func (c *Config) complete() {
	c.Agent = strings.TrimSpace(c.Agent)
	c.Profile = strings.TrimSpace(c.Profile)

	for i := range c.Agents {
		c.Agents[i].Name = strings.TrimSpace(c.Agents[i].Name)
		c.Agents[i].Type = strings.TrimSpace(c.Agents[i].Type)
		c.Agents[i].Binary = strings.TrimSpace(c.Agents[i].Binary)
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
				Binary: base.Binary,
				Model:  base.Model,
				Params: cloneParams(base.Params),
				Envs:   maps.Clone(base.Envs),
			}

			if v := strings.TrimSpace(role.Agent.Type); v != "" {
				resolved.Type = v
			}
			if v := strings.TrimSpace(role.Agent.Binary); v != "" {
				resolved.Binary = v
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
		// Write back: a future append to Roles would reallocate only the copy.
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

// InitConfig writes the default config to ~/.mecha/config.yaml, creating the
// directory if needed. An existing file is renamed to .bak unless force is set.
func InitConfig(force bool) (path string, err error) {
	dir, err := MechaDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("config: cannot create directory %q: %w", dir, err)
	}

	path = filepath.Join(dir, configFileName)

	if _, statErr := os.Stat(path); statErr == nil {
		if force {
			if err := os.WriteFile(path, defaultConfigYAML, 0o644); err != nil {
				return "", fmt.Errorf("config: cannot write %q: %w", path, err)
			}
			return path, nil
		}

		bakPath := path + ".bak"
		os.Remove(bakPath) // remove old bak if any
		if err := os.Rename(path, bakPath); err != nil {
			return "", fmt.Errorf("config: cannot rename %q to %q: %w", path, bakPath, err)
		}
	}

	if err := os.WriteFile(path, defaultConfigYAML, 0o644); err != nil {
		return "", fmt.Errorf("config: cannot write %q: %w", path, err)
	}

	return path, nil
}

func cloneParams(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}
