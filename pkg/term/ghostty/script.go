package ghostty

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/champly/mecha/pkg/term/driver"
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

// captureTemp captures the terminal content by writing to a temp file.
func captureTemp(ctx context.Context, termID, action string) (string, error) {
	if _, err := runAppleScript(ctx, actionScript(termID, action)); err != nil {
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

func firstSpawnScript(cmdLine string) string {
	w := initialInput(cmdLine, "newTerminal")
	return wrapAppleScript(app, fmt.Sprintf(`
			set w to front window
			set srcTerminal to focused terminal of selected tab of w
			perform action %s on srcTerminal
			set terminalCount to count of terminals of selected tab of w
			set newTerminal to terminal terminalCount of selected tab of w%s
			return (id of w) & "|" & (id of newTerminal)
		`, quoteAppleScript("new_split:right"), w))
}

func splitSpawnScript(windowID, targetID, cmdLine string) string {
	w := initialInput(cmdLine, "newTerminal")
	return wrapAppleScript(app, fmt.Sprintf(`
			set w to window id %s
			set targetTerminal to terminal id %s of selected tab of w
			perform action %s on targetTerminal
			set terminalCount to count of terminals of selected tab of w
			set newTerminal to terminal terminalCount of selected tab of w%s
			return id of newTerminal
		`, quoteAppleScript(windowID), quoteAppleScript(targetID), quoteAppleScript("new_split:down"), w))
}

func textScript(terminalID, text string) string {
	cmds := driver.ScriptMultiline(text,
		func(part string) string {
			return fmt.Sprintf(`	input text %s to targetTerminal`, quoteAppleScript(part))
		},
		enter,
	)
	if len(cmds) == 0 {
		return ""
	}
	return wrapAppleScript(app, fmt.Sprintf(`
			set targetTerminal to terminal id %s
		%s
		`, quoteAppleScript(terminalID), strings.Join(cmds, "\n")))
}

func initialInput(cmdLine, varName string) string {
	if cmdLine == "" {
		return ""
	}
	return fmt.Sprintf(`
			input text %s to %s
			send key "enter" to %s`, quoteAppleScript(cmdLine), varName, varName)
}

func actionScript(terminalID, action string) string {
	return wrapAppleScript(app, fmt.Sprintf(`
			set targetTerminal to terminal id %s
			perform action %s on targetTerminal
		`, quoteAppleScript(terminalID), quoteAppleScript(action)))
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
