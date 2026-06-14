package term

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

const (
	tmuxBinaryName = "tmux"
	tmuxSplitSize  = "50"
	tmuxPaneIDFmt  = `#{pane_id}`
	tmuxErrFmt     = `term/tmux: %s %s failed: %w: %s`
)

type TmuxProvider struct {
	mu sync.Mutex

	anchorPane string
	rightPanes paneChain
}

func NewTmuxProvider() *TmuxProvider {
	return &TmuxProvider{}
}

func (p *TmuxProvider) Spawn(ctx context.Context, spec PaneSpec) (PaneHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.anchorPane == "" {
		anchor, err := p.currentPaneID(ctx)
		if err != nil {
			return nil, err
		}
		p.anchorPane = anchor
	}

	var paneID string
	var err error

	if p.rightPanes.Empty() {
		paneID, err = p.splitRight(ctx, p.anchorPane, spec.WorkDir)
	} else {
		paneID, err = p.splitDown(ctx, p.rightPanes.Last(), spec.WorkDir)
	}
	if err != nil {
		return nil, err
	}

	if bootstrap := buildShellBootstrap(spec); bootstrap != "" {
		if err := p.sendLiteral(ctx, paneID, bootstrap); err != nil {
			return nil, err
		}
		if err := p.sendEnter(ctx, paneID); err != nil {
			return nil, err
		}
	}

	p.rightPanes.Push(paneID)
	return newHandle("tmux", paneID), nil
}

func (p *TmuxProvider) Send(ctx context.Context, handle PaneHandle, text string) error {
	return p.sendMultiline(ctx, handle.PaneID(), text)
}

func (p *TmuxProvider) sendMultiline(ctx context.Context, paneID, text string) error {
	return applyMultilineInput(text,
		func(part string) error {
			return p.sendLiteral(ctx, paneID, part)
		},
		func() error {
			return p.sendEnter(ctx, paneID)
		},
	)
}

func (p *TmuxProvider) sendLiteral(ctx context.Context, paneID, text string) error {
	if text == "" {
		return nil
	}
	_, err := p.tmux(ctx, "send-keys", "-t", paneID, "-l", text)
	return err
}

func (p *TmuxProvider) sendEnter(ctx context.Context, paneID string) error {
	_, err := p.tmux(ctx, "send-keys", "-t", paneID, "C-m")
	return err
}

func (p *TmuxProvider) Capture(ctx context.Context, handle PaneHandle) (string, error) {
	return p.tmux(ctx, tmuxCaptureArgs(false, handle.PaneID())...)
}

func (p *TmuxProvider) CaptureAll(ctx context.Context, handle PaneHandle) (string, error) {
	return p.tmux(ctx, tmuxCaptureArgs(true, handle.PaneID())...)
}

func (p *TmuxProvider) Kill(ctx context.Context, handle PaneHandle) error {
	_, err := p.tmux(ctx, "kill-pane", "-t", handle.PaneID())
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.rightPanes.Remove(handle.PaneID())
	return nil
}

func (p *TmuxProvider) currentPaneID(ctx context.Context) (string, error) {
	out, err := p.tmux(ctx, "display-message", "-p", tmuxPaneIDFmt)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("term/tmux: empty current pane id")
	}
	return id, nil
}

func (p *TmuxProvider) splitRight(ctx context.Context, targetPane, workDir string) (string, error) {
	out, err := p.tmux(ctx, tmuxSplitArgs("-h", targetPane, workDir)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *TmuxProvider) splitDown(ctx context.Context, targetPane, workDir string) (string, error) {
	out, err := p.tmux(ctx, tmuxSplitArgs("-v", targetPane, workDir)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func tmuxSplitArgs(direction, targetPane, workDir string) []string {
	args := []string{"split-window", direction, "-p", tmuxSplitSize, "-P", "-F", tmuxPaneIDFmt, "-t", targetPane}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	return args
}

func tmuxCaptureArgs(all bool, paneID string) []string {
	args := []string{"capture-pane", "-p"}
	if all {
		args = append(args, "-S", "-")
	}
	args = append(args, "-t", paneID)
	return args
}

func (p *TmuxProvider) tmux(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, tmuxBinaryName, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(tmuxErrFmt, tmuxBinaryName, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
