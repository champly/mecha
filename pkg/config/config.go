package config

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// LogPath returns the log file path for a workspace, matching the convention
// used by both core and agentd so all process logs land in the same file.
func LogPath(workspace string) (string, error) {
	dir, err := MechaDir()
	if err != nil {
		return "", err
	}

	name := strings.TrimLeft(workspace, "/")
	name = strings.ReplaceAll(name, "/", "_")
	logDir := filepath.Join(dir, "logs", name)

	return filepath.Join(logDir, time.Now().Format(time.DateOnly)+".log"), nil
}

// NewFileLogger opens the log file for workspace and returns a configured
// slog.Logger that writes structured text logs to it. Both core and agentd
// use this so all process logs land in the same file.
func NewFileLogger(workspace string) (*slog.Logger, *os.File, error) {
	path, err := LogPath(workspace)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("config: create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("config: open log file: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.SourceKey {
				if src, ok := a.Value.Any().(*slog.Source); ok {
					src.File = filepath.Base(src.File)
				}
			}
			return a
		},
	}))

	return logger, f, nil
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

// LoadConfig reads YAML config from path, validates it, and completes it with
// defaults. If path is empty, ~/.mecha/config.yaml is used. validType
// validates agent type strings; pass agent.ValidateAgentType to reject
// unknown types at startup.
func LoadConfig(path string, validType func(string) bool) (Config, error) {
	c, err := parseConfigFile(path)
	if err != nil {
		return Config{}, err
	}

	if err := c.validate(validType); err != nil {
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
// references, and exactly one coordinator role per profile. validType
// validates agent type strings (nil skips).
func (c Config) validate(validType func(string) bool) error {
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

		if validType != nil {
			agentType := strings.TrimSpace(agent.Type)
			if !validType(agentType) {
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

	profile := strings.TrimSpace(c.Profile)
	if profile == "" {
		return fmt.Errorf("config: profile is required")
	}
	if _, ok := c.Profiles[profile]; !ok {
		return fmt.Errorf("config: profile %q not found in profiles", profile)
	}

	for profileName, profile := range c.Profiles {
		coordinatorCount := 0
		roleNames := make(map[string]struct{}, len(profile.Roles))
		for _, role := range profile.Roles {
			if role.IsCoordinator {
				coordinatorCount++
			}

			roleName := strings.TrimSpace(role.Name)
			if roleName == "" {
				return fmt.Errorf("config: profile %q has a role with empty name", profileName)
			}
			if _, exists := roleNames[roleName]; exists {
				return fmt.Errorf("config: duplicate role name %q in profile %q", roleName, profileName)
			}
			roleNames[roleName] = struct{}{}

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

			if validType != nil {
				if typ := strings.TrimSpace(role.Agent.Type); typ != "" && !validType(typ) {
					return fmt.Errorf("config: role %q in profile %q: unknown agent type %q", role.Name, profileName, typ)
				}
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
			// Role-level params/envs merge over the agent-level ones
			// (role wins per key), not wholesale replacement.
			if role.Agent.Params != nil {
				maps.Copy(resolved.Params, role.Agent.Params)
			}
			if role.Agent.Envs != nil {
				if resolved.Envs == nil {
					resolved.Envs = map[string]string{}
				}
				maps.Copy(resolved.Envs, role.Agent.Envs)
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
