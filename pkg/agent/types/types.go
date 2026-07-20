package types

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/champly/mecha/pkg/config"
)

// AgentContext bundles the runtime environment for an agent instance.
type AgentContext struct {
	Workspace   string // project root (cmd.Dir)
	RoleDir     string // agent-specific files (CLAUDE.md, settings.json)
	Prompt      string // role instruction (injected via --append-system-prompt-file)
	WebhookAddr string // agentd address to POST hook events to
}

// Factory creates an Agent from the given parameters.
type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)

// Agent is the interface all agent types must implement.
type Agent interface {
	Prepare() error
	Cmd() *exec.Cmd
	ParseHookEvent(raw []byte) (HookEvent, error)
}

const (
	EventSessionStart  = "SessionStart"
	EventPostToolBatch = "PostToolBatch"
	EventStop          = "Stop"
	EventStopFailure   = "StopFailure"
)

type HookEvent struct {
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

// BuildEnv returns the process environment overlaid with defaults and user
// values (later layers win), sorted for deterministic output.
func BuildEnv(user, defaults map[string]string) []string {
	merged := make(map[string]string, len(defaults)+len(user))
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			merged[k] = v
		}
	}
	for k, v := range MergeMap(user, defaults) {
		merged[k] = v
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+merged[k])
	}
	return env
}
