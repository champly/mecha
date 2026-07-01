package types

import (
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"sort"

	"github.com/champly/mecha/pkg/config"
)

// AgentContext bundles the runtime environment for an agent instance:
//   - Workspace is the project root (cmd.Dir).
//   - RoleDir is the directory for agent-specific files (CLAUDE.md, settings.json).
//   - Prompt is the role-specific instruction (injected via --append-system-prompt-file).
//   - AgentID is the unique identifier for this agent instance.
type AgentContext struct {
	Workspace string
	RoleDir   string
	Prompt    string
	AgentID   string
}

// Factory creates an Agent from the given parameters.
type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)

// Agent is the interface all agent types must implement.
type Agent interface {
	Prepare() error
	Cmd() *exec.Cmd
	ParseHookEvent(raw []byte) (HookEvent, error)
	ID() string
}

const (
	EventSessionStart  = "SessionStart"
	EventPostToolBatch = "PostToolBatch"
	EventStop          = "Stop"
	EventStopFailure   = "StopFailure"
)

type HookEvent struct {
	AgentID      string          `json:"agent_id"`
	Event        string          `json:"event"`
	SessionID    string          `json:"session_id,omitempty"`
	Output       string          `json:"output,omitempty"`
	OutputSource string          `json:"output_source,omitempty"`
	Error        string          `json:"error,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

// MergeMap returns a new map with defaults overridden by user values.
func MergeMap[M ~map[K]V, K comparable, V any](user, defaults M) M {
	if len(defaults) == 0 {
		return maps.Clone(user)
	}
	r := maps.Clone(defaults)
	maps.Copy(r, user)
	return r
}

// BuildArgs merges user params over defaults, then returns them as CLI
// --key value arguments with keys sorted for deterministic output.
// Bool(true) values produce --key without a value.
func BuildArgs(user, defaults map[string]any) []string {
	params := MergeMap(user, defaults)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		v := params[k]
		if b, ok := v.(bool); ok && b {
			args = append(args, "--"+k)
		} else {
			args = append(args, "--"+k, fmt.Sprint(v))
		}
	}
	return args
}
