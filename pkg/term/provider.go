package term

import "context"

// PaneBackend abstracts terminal multiplexers or PTY backends (e.g. tmux, iTerm2, Ghostty).
// All operations are scoped to a PaneHandle returned by Spawn.
type PaneBackend interface {
	// Spawn creates a new terminal pane according to spec and returns a PaneHandle
	// that identifies it for subsequent operations.
	Spawn(ctx context.Context, spec PaneSpec) (PaneHandle, error)

	// Send injects text into the pane as if typed on the keyboard.
	// The caller is responsible for appending "\n" when a newline is needed.
	Send(ctx context.Context, handle PaneHandle, text string) error

	// Capture returns the content currently visible in the pane's viewport.
	// Use this for lightweight polling (e.g. detecting a prompt).
	Capture(ctx context.Context, handle PaneHandle) (string, error)

	// CaptureAll returns the full scrollback history of the pane.
	// Use this to extract complete task output after the agent finishes.
	CaptureAll(ctx context.Context, handle PaneHandle) (string, error)

	// Kill forcibly terminates the pane and releases associated resources.
	Kill(ctx context.Context, handle PaneHandle) error
}

// PaneSpec describes how a new terminal pane should be created.
type PaneSpec struct {
	// WorkDir is the initial working directory for the process.
	WorkDir string
	// Command is the executable and its arguments, e.g. ["claude", "--resume", "abc"].
	Command []string
	// Env holds additional environment variables to inject into the process.
	Env map[string]string
}

// PaneHandle identifies a running terminal pane.
type PaneHandle interface {
	// ID returns the mecha-internal unique identifier for this pane.
	ID() string
	// PaneID returns the backend-native identifier (e.g. tmux pane "%12").
	PaneID() string
}
