package types

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/champly/mecha/pkg/config"
)

// shellSafeToken matches strings that need no shell quoting.
var shellSafeToken = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

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
	Event     string `json:"event"`
	SessionID string `json:"session_id,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Base carries the runtime fields shared by all agent drivers.
type Base struct {
	Workspace   string
	RoleDir     string
	Prompt      string
	Cfg         config.AgentConfig
	MechaBinary string
	WebhookAddr string
}

// NewBase populates a Base from the factory inputs.
func NewBase(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) Base {
	return Base{
		Workspace:   ctx.Workspace,
		RoleDir:     ctx.RoleDir,
		Prompt:      ctx.Prompt,
		Cfg:         cfg,
		MechaBinary: runtime.MechaBinary,
		WebhookAddr: ctx.WebhookAddr,
	}
}

// PrepareRoleFile creates the role dir and writes the role prompt to filename.
func (b *Base) PrepareRoleFile(filename string) error {
	if err := os.MkdirAll(b.RoleDir, 0o755); err != nil {
		return fmt.Errorf("create dir %q: %w", b.RoleDir, err)
	}
	if err := os.WriteFile(filepath.Join(b.RoleDir, filename), []byte(b.Prompt), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return nil
}

// ResolveBinary resolves the agent binary (cfg.Binary or defaultBinary) and
// verifies it exists, failing at factory time instead of at PTY launch.
func (b *Base) ResolveBinary(defaultBinary string) (string, error) {
	binary := b.Cfg.Binary
	if binary == "" {
		binary = defaultBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return "", fmt.Errorf("agent binary %q: %w", binary, err)
	}
	return binary, nil
}

// NewAgentCmd builds the process command with the model flag, merged
// params/envs, binary fallback, and workspace directory. Drivers append their
// own flags and may override cmd.Dir.
func (b *Base) NewAgentCmd(defaultBinary string, defaultParams map[string]any, defaultEnvs map[string]string) *exec.Cmd {
	args := make([]string, 0, 8)
	if b.Cfg.Model != "" {
		args = append(args, "--model", b.Cfg.Model)
	}
	args = append(args, BuildArgs(b.Cfg.Params, defaultParams)...)

	binary := b.Cfg.Binary
	if binary == "" {
		binary = defaultBinary
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = b.Workspace
	cmd.Env = BuildEnv(b.Cfg.Envs, defaultEnvs)
	return cmd
}

// WriteJSONFile atomically writes v as indented JSON to path, creating parent
// dirs. A failed write never leaves a truncated file behind.
func WriteJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir %q: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mecha-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

// QuoteShell quotes s for safe use in a shell command. Shell-safe tokens are
// returned unchanged; everything else is wrapped in single quotes with
// embedded quotes escaped as '\''.
func QuoteShell(s string) string {
	if s == "" {
		return `''`
	}
	if shellSafeToken.MatchString(s) {
		return s
	}
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

// WebhookCommand returns the shell command line an agent hook runs to deliver
// events to the agentd webhook server.
func WebhookCommand(binary, addr string) string {
	return fmt.Sprintf("%s webhook --addr %s", QuoteShell(binary), QuoteShell(addr))
}

// ParseHook parses raw provider hook JSON: resolves hook_event_name through
// eventMap, extracts session_id, then lets extract fill event-specific fields.
// prefix names the provider (e.g. "claude") so errors in the shared webhook
// log are attributable to one agent type.
func ParseHook(prefix string, raw []byte, eventMap map[string]string, extract func(m map[string]any, e *HookEvent)) (HookEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return HookEvent{}, fmt.Errorf("%s: parse hook event: %w", prefix, err)
	}

	hookEventName, ok := m["hook_event_name"].(string)
	if !ok {
		return HookEvent{}, fmt.Errorf("%s: hook_event_name missing or not a string", prefix)
	}

	event, ok := eventMap[hookEventName]
	if !ok {
		return HookEvent{}, fmt.Errorf("%s: unknown hook event %q", prefix, hookEventName)
	}

	e := HookEvent{Event: event}
	if sid, ok := m["session_id"].(string); ok {
		e.SessionID = sid
	}
	if extract != nil {
		extract(m, &e)
	}
	return e, nil
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
// Bool(true) values produce a bare --key flag; bool(false) values are
// omitted entirely, so a default true can be overridden with false
// (a "--key false" pair would leave the flag set and leak "false" as a
// positional argument on pflag-style CLIs).
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
		if b, ok := v.(bool); ok {
			if b {
				args = append(args, "--"+k)
			}
			continue
		}
		args = append(args, "--"+k, fmt.Sprint(v))
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
