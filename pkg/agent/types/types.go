package types

import (
	"encoding/json"
	"os/exec"

	"github.com/champly/mecha/pkg/config"
)

// AgentContext bundles the runtime environment for an agent instance:
//   - Workspace is the project root (cmd.Dir).
//   - RoleDir is the CLAUDE_CONFIG_DIR target (settings, sessions, memory).
//   - Prompt is the role-specific instruction (written to CLAUDE.md).
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
