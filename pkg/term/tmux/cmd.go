package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func splitRight(ctx context.Context, target, workDir, command string) (string, error) {
	out, err := tmux(ctx, splitArgs("-h", target, workDir, command)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func splitDown(ctx context.Context, target, workDir, command string) (string, error) {
	out, err := tmux(ctx, splitArgs("-v", target, workDir, command)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func splitArgs(dir, target, workDir, command string) []string {
	args := []string{"split-window", dir, "-p", splitSize, "-P", "-F", paneFmt, "-t", target}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if command != "" {
		args = append(args, command)
	}
	return args
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
