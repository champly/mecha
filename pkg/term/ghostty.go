package term

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	ghosttyProviderPrefix = "ghostty"
	ghosttyAppName        = "Ghostty"
	ghosttyEnterKey       = "enter"
	ghosttyEnterCmd       = `	send key "enter" to targetTerminal`
)

type GhosttyProvider struct {
	mu sync.Mutex

	windowID  string
	terminals paneChain
}

func NewGhosttyProvider() *GhosttyProvider {
	return &GhosttyProvider{}
}

func (p *GhosttyProvider) Spawn(ctx context.Context, spec PaneSpec) (PaneHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	bootstrap := buildShellBootstrap(spec)

	if p.windowID == "" || p.terminals.Empty() {
		out, err := runAppleScript(ctx, buildGhosttyFirstSpawnScript(bootstrap))
		if err != nil {
			return nil, err
		}
		windowID, terminalID, err := parseGhosttySpawnResult(out)
		if err != nil {
			return nil, err
		}
		p.windowID = windowID
		p.terminals.Push(terminalID)
		return newHandle(ghosttyProviderPrefix, terminalID), nil
	}

	targetTerminalID := p.terminals.Last()
	out, err := runAppleScript(ctx, buildGhosttySpawnSplitScript(p.windowID, targetTerminalID, bootstrap))
	if err != nil {
		return nil, err
	}
	terminalID := strings.TrimSpace(out)
	if terminalID == "" {
		return nil, fmt.Errorf("term/ghostty: empty terminal id after split")
	}
	p.terminals.Push(terminalID)
	return newHandle(ghosttyProviderPrefix, terminalID), nil
}

func (p *GhosttyProvider) Send(ctx context.Context, handle PaneHandle, text string) error {
	script := buildGhosttyTextScript(handle.PaneID(), text)
	if script == "" {
		return nil
	}
	_, err := runAppleScript(ctx, script)
	return err
}

func (p *GhosttyProvider) Capture(ctx context.Context, handle PaneHandle) (string, error) {
	return p.captureViaTempFile(ctx, handle.PaneID(), "write_screen_file:copy")
}

func (p *GhosttyProvider) CaptureAll(ctx context.Context, handle PaneHandle) (string, error) {
	return p.captureViaTempFile(ctx, handle.PaneID(), "write_scrollback_file:copy")
}

func (p *GhosttyProvider) Kill(ctx context.Context, handle PaneHandle) error {
	_, err := runAppleScript(ctx, buildGhosttyCloseScript(handle.PaneID()))
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminals.Remove(handle.PaneID())
	if p.terminals.Empty() {
		p.windowID = ""
	}
	return nil
}

func (p *GhosttyProvider) captureViaTempFile(ctx context.Context, terminalID, action string) (string, error) {
	if _, err := runAppleScript(ctx, buildGhosttyActionScript(terminalID, action)); err != nil {
		return "", err
	}
	pathOut, err := runAppleScript(ctx, `do shell script "pbpaste"`)
	if err != nil {
		return "", fmt.Errorf("term/ghostty: pbpaste failed: %w", err)
	}
	path := strings.TrimSpace(pathOut)
	if path == "" {
		return "", fmt.Errorf("term/ghostty: empty capture file path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("term/ghostty: read capture file %s: %w", path, err)
	}
	return string(data), nil
}

func buildGhosttyFirstSpawnScript(cmdLine string) string {
	write := buildGhosttyInitialInput(cmdLine, "newTerminal")
	body := fmt.Sprintf(`
	set w to front window
	set srcTerminal to focused terminal of selected tab of w
	perform action %s on srcTerminal
	set terminalCount to count of terminals of selected tab of w
	set newTerminal to terminal terminalCount of selected tab of w%s
	return (id of w) & "|" & (id of newTerminal)
`, appleScriptString("new_split:right"), write)
	return wrapAppScript(ghosttyAppName, body)
}

func buildGhosttySpawnSplitScript(windowID, targetTerminalID, cmdLine string) string {
	write := buildGhosttyInitialInput(cmdLine, "newTerminal")
	body := fmt.Sprintf(`
	set w to window id %s
	set targetTerminal to terminal id %s of selected tab of w
	perform action %s on targetTerminal
	set terminalCount to count of terminals of selected tab of w
	set newTerminal to terminal terminalCount of selected tab of w%s
	return id of newTerminal
`, appleScriptString(windowID), appleScriptString(targetTerminalID), appleScriptString("new_split:down"), write)
	return wrapAppScript(ghosttyAppName, body)
}

func buildGhosttyTextScript(terminalID, text string) string {
	commands := buildMultilineCommands(
		text,
		func(part string) string {
			return fmt.Sprintf(`	input text %s to targetTerminal`, appleScriptString(part))
		},
		ghosttyEnterCmd,
	)
	if len(commands) == 0 {
		return ""
	}
	body := fmt.Sprintf(`
	set targetTerminal to terminal id %s
%s
`, appleScriptString(terminalID), strings.Join(commands, "\n"))
	return wrapAppScript(ghosttyAppName, body)
}

func buildGhosttyInitialInput(cmdLine, terminalVar string) string {
	if cmdLine == "" {
		return ""
	}
	return fmt.Sprintf(`
	input text %s to %s
	send key %s to %s`, appleScriptString(cmdLine), terminalVar, appleScriptString(ghosttyEnterKey), terminalVar)
}

func buildGhosttyActionScript(terminalID, action string) string {
	body := fmt.Sprintf(`
	set targetTerminal to terminal id %s
	perform action %s on targetTerminal
`, appleScriptString(terminalID), appleScriptString(action))
	return wrapAppScript(ghosttyAppName, body)
}

func buildGhosttyCloseScript(terminalID string) string {
	body := fmt.Sprintf(`
	set targetTerminal to terminal id %s
	close targetTerminal
`, appleScriptString(terminalID))
	return wrapAppScript(ghosttyAppName, body)
}

func parseGhosttySpawnResult(out string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("term/ghostty: unexpected spawn result %q", strings.TrimSpace(out))
	}
	windowID := strings.TrimSpace(parts[0])
	terminalID := strings.TrimSpace(parts[1])
	if windowID == "" || terminalID == "" {
		return "", "", fmt.Errorf("term/ghostty: empty spawn result %q", strings.TrimSpace(out))
	}
	return windowID, terminalID, nil
}
