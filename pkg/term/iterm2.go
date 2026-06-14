package term

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	iterm2AppName  = "iTerm2"
	iterm2EnterCmd = `		write text ""`
)

type ITerm2Provider struct {
	mu sync.Mutex

	rightWindowID string
	sessions      paneChain
}

func NewITerm2Provider() *ITerm2Provider {
	return &ITerm2Provider{}
}

func (p *ITerm2Provider) Spawn(ctx context.Context, spec PaneSpec) (PaneHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cmdLine := buildShellBootstrap(spec)

	var script string
	if p.sessions.Empty() {
		script = buildITerm2SpawnRightScript(cmdLine)
	} else {
		script = buildITerm2SpawnDownScript(p.rightWindowID, p.sessions.Last(), cmdLine)
	}

	out, err := runAppleScript(ctx, script)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(out)
	windowID, err := currentWindowID(ctx)
	if err != nil {
		return nil, err
	}
	p.rightWindowID = strings.TrimSpace(windowID)
	p.sessions.Push(sessionID)
	return newHandle("iterm2", sessionID), nil
}

func (p *ITerm2Provider) Send(ctx context.Context, handle PaneHandle, text string) error {
	script := buildITerm2SendKeysScript(p.rightWindowID, handle.PaneID(), text)
	if script == "" {
		return nil
	}
	_, err := runAppleScript(ctx, script)
	return err
}

func (p *ITerm2Provider) Capture(ctx context.Context, handle PaneHandle) (string, error) {
	script := buildITerm2CaptureScript(p.rightWindowID, handle.PaneID())
	return runAppleScript(ctx, script)
}

func (p *ITerm2Provider) CaptureAll(ctx context.Context, handle PaneHandle) (string, error) {
	return p.Capture(ctx, handle)
}

func (p *ITerm2Provider) Kill(ctx context.Context, handle PaneHandle) error {
	script := buildITerm2KillScript(p.rightWindowID, handle.PaneID())
	_, err := runAppleScript(ctx, script)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions.Remove(handle.PaneID())
	if p.sessions.Empty() {
		p.rightWindowID = ""
	}
	return nil
}

func currentWindowID(ctx context.Context) (string, error) {
	out, err := runAppleScript(ctx, fmt.Sprintf(`tell application %q to return id of current window`, iterm2AppName))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func buildITerm2SpawnRightScript(cmdLine string) string {
	write := buildITerm2SpawnWrite(cmdLine)
	body := fmt.Sprintf(`
	tell current session of current tab of current window
		set newSession to (split vertically with default profile)
	end tell
	tell newSession%s
		return id as text
	end tell
`, write)
	return wrapAppScript(iterm2AppName, body)
}

func buildITerm2SpawnDownScript(windowID, baseSessionID, cmdLine string) string {
	write := buildITerm2SpawnWrite(cmdLine)
	body := fmt.Sprintf(`
	tell session id %s of tab 1 of window id %s
		set newSession to (split horizontally with default profile)
	end tell
	tell newSession%s
		return id as text
	end tell
`, appleScriptString(baseSessionID), windowID, write)
	return wrapAppScript(iterm2AppName, body)
}

func buildITerm2SendKeysScript(windowID, sessionID, text string) string {
	commands := buildMultilineCommands(
		text,
		func(part string) string {
			return fmt.Sprintf(`		write text %s newline NO`, appleScriptString(part))
		},
		iterm2EnterCmd,
	)
	if len(commands) == 0 {
		return ""
	}

	body := fmt.Sprintf(`
	tell session id %s of tab 1 of window id %s
%s
	end tell
`, appleScriptString(sessionID), windowID, strings.Join(commands, "\n"))
	return wrapAppScript(iterm2AppName, body)
}

func buildITerm2SpawnWrite(cmdLine string) string {
	if cmdLine == "" {
		return ""
	}
	return fmt.Sprintf(`
		write text %s`, appleScriptString(cmdLine))
}

func buildITerm2CaptureScript(windowID, sessionID string) string {
	body := fmt.Sprintf(`
	return contents of session id %s of tab 1 of window id %s
`, appleScriptString(sessionID), windowID)
	return wrapAppScript(iterm2AppName, body)
}

func buildITerm2KillScript(windowID, sessionID string) string {
	body := fmt.Sprintf(`
	tell session id %s of tab 1 of window id %s
		close
	end tell
`, appleScriptString(sessionID), windowID)
	return wrapAppScript(iterm2AppName, body)
}
