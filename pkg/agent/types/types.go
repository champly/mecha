package types

import (
	"encoding/json"
	"os/exec"

	"github.com/champly/mecha/pkg/config"
)

// Factory creates an Agent from the given parameters.
type Factory func(roleDir, agentID, prompt string, cfg config.AgentConfig) (Agent, error)

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
