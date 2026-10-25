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

// quoteAppleScript quotes s as an AppleScript string literal. Backslash
// must be escaped first: AppleScript silently drops an unescaped one.
func quoteAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// listTerminalsScript returns every application terminal as an "id|tty" line
// so the process's controlling tty can be mapped to a unique terminal id.
func listTerminalsScript() string {
	return wrapAppleScript(app, `
			set out to ""
			repeat with term in terminals
				set out to out & (id of term) & "|" & (tty of term) & linefeed
			end repeat
			return out
		`)
}

// anchorSpawnScript splits the pinned terminal, addressed globally by id so
// it resolves even when it is not in the window's selected tab. Returns the
// new terminal's id.
func anchorSpawnScript(targetID, cmdLine string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set targetTerminal to terminal id %s
			set newTerminal to split targetTerminal direction right%s
			return id of newTerminal
		`, quoteAppleScript(targetID), initialInput(cmdLine, "newTerminal")))
}

// splitSpawnScript splits an existing terminal, addressed globally by id.
func splitSpawnScript(targetID, cmdLine string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set targetTerminal to terminal id %s
			set newTerminal to split targetTerminal direction down%s
			return id of newTerminal
		`, quoteAppleScript(targetID), initialInput(cmdLine, "newTerminal")))
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
