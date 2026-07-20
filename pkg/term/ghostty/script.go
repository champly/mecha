package ghostty

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func runAppleScript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("term/ghostty: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func wrapAppleScript(app, body string) string {
	return fmt.Sprintf(`tell application %q
%s
end tell`, app, body)
}

func quoteAppleScript(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func firstSpawnScript(cmdLine string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set w to front window
			set srcTerminal to focused terminal of selected tab of w
			set newTerminal to split srcTerminal direction right%s
			return (id of w) & "|" & (id of newTerminal)
		`, initialInput(cmdLine, "newTerminal")))
}

func splitSpawnScript(windowID, targetID, cmdLine string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set w to window id %s
			set targetTerminal to terminal id %s of selected tab of w
			set newTerminal to split targetTerminal direction down%s
			return id of newTerminal
		`, quoteAppleScript(windowID), quoteAppleScript(targetID), initialInput(cmdLine, "newTerminal")))
}

func initialInput(cmdLine, varName string) string {
	if cmdLine == "" {
		return ""
	}
	return fmt.Sprintf(`
			input text %s to %s
			send key "enter" to %s`, quoteAppleScript(cmdLine), varName, varName)
}

func closeScript(terminalID string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set targetTerminal to terminal id %s
			close targetTerminal
		`, quoteAppleScript(terminalID)))
}

func parseSpawnResult(out string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("term/ghostty: unexpected spawn result %q", strings.TrimSpace(out))
	}
	winID := strings.TrimSpace(parts[0])
	termID := strings.TrimSpace(parts[1])
	if winID == "" || termID == "" {
		return "", "", fmt.Errorf("term/ghostty: empty spawn result %q", strings.TrimSpace(out))
	}
	return winID, termID, nil
}
