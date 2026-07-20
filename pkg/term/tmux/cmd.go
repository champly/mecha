package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func currentPane(ctx context.Context) (string, error) {
	out, err := tmux(ctx, "display-message", "-p", paneFmt)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("term/tmux: empty current pane id")
	}
	return id, nil
}

func splitRight(ctx context.Context, target, workDir string) (string, error) {
	out, err := tmux(ctx, splitArgs("-h", target, workDir)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func splitDown(ctx context.Context, target, workDir string) (string, error) {
	out, err := tmux(ctx, splitArgs("-v", target, workDir)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func splitArgs(dir, target, workDir string) []string {
	args := []string{"split-window", dir, "-p", splitSize, "-P", "-F", paneFmt, "-t", target}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	return args
}

func sendLiteral(ctx context.Context, paneID, text string) error {
	if text == "" {
		return nil
	}
	_, err := tmux(ctx, "send-keys", "-t", paneID, "-l", text)
	return err
}

func sendEnter(ctx context.Context, paneID string) error {
	// C-m (\r) alone causes carriage return without line feed.
	// C-m followed by C-j (\n) gives the terminal both \r\n.
	_, err := tmux(ctx, "send-keys", "-t", paneID, "C-m", "C-j")
	return err
}

func tmux(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(errFmt, binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
