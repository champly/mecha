package agentd

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/champly/mecha/pkg/agent"
	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// startAgent creates the agent, prepares its role directory, and launches it
// with a PTY attached to agentd's stdio.
func (a *Agentd) startAgent(cfg *api.RegisterResponse, webhookAddr string) error {
	ag, err := agent.NewFromConfig(
		cfg.Workspace,
		cfg.Prompt,
		cfg.RoleName,
		webhookAddr,
		config.AgentConfig{
			Type:   cfg.Agent.Type,
			Binary: cfg.Agent.Binary,
			Model:  cfg.Agent.Model,
			Params: cfg.Agent.Params.AsMap(),
			Envs:   cfg.Agent.Envs,
		},
		cfg.MechaBinary,
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if err := ag.Prepare(); err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	a.webhook.SetParseFunc(ag.ParseHookEvent)

	cmd := ag.Cmd()
	ptmx, err := launchPTY(cmd)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.ptmx = ptmx
	a.mu.Unlock()

	restore := makeRawIfTerminal()

	out := &activityWriter{w: os.Stdout, last: &a.lastOutput}
	go io.Copy(out, ptmx)
	go io.Copy(ptmx, os.Stdin)
	go a.watchWinch()
	go a.watchReady()
	go a.waitAgent(cmd, restore)

	return nil
}

// activityWriter records the time of each write, used to detect when the
// agent's output goes quiet.
type activityWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.last.Store(time.Now().UnixNano())
	return a.w.Write(p)
}

const (
	readyQuietPeriod = 1500 * time.Millisecond
	readyTimeout     = 30 * time.Second
)

// watchReady closes a.ready once the agent's TUI finishes its initial render
// (output quiet for readyQuietPeriod). Tasks written earlier are swallowed by
// the agent's input initialization: the text lands in the input box but the
// enter key is ignored.
func (a *Agentd) watchReady() {
	defer close(a.ready)
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		last := a.lastOutput.Load()
		if last != 0 && time.Since(time.Unix(0, last)) > readyQuietPeriod {
			return
		}
		select {
		case <-a.stop:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	slog.Warn("agent readiness wait timed out; tasks will be written anyway", "id", a.opts.ID)
}

// launchPTY starts cmd with a PTY and returns the PTY master.
func launchPTY(cmd *exec.Cmd) (*os.File, error) {
	sz, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		sz = &pty.Winsize{Rows: 24, Cols: 80}
	}

	ptmx, err := pty.StartWithSize(cmd, sz)
	if err != nil {
		return nil, fmt.Errorf("start with pty: %w", err)
	}
	time.Sleep(time.Millisecond * 100) // give the agent a moment to initialize its TUI

	return ptmx, nil
}

// makeRawIfTerminal switches stdin to raw mode when it is a terminal, so
// keystrokes and terminal query replies reach the agent's TUI unmodified.
// Returns the restore function (no-op when stdin is not a terminal).
func makeRawIfTerminal() func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}
	return func() { _ = term.Restore(fd, oldState) }
}

// waitAgent waits for the agent to exit, restores the terminal, fails any
// in-flight task, then closes the PTY and signals shutdown.
func (a *Agentd) waitAgent(cmd *exec.Cmd, restore func()) {
	cmd.Wait()
	restore()

	a.mu.Lock()
	if a.hasTask {
		a.taskCh <- taskResult{result: "agent exited during task"}
	}
	a.mu.Unlock()

	a.ptmx.Close()
	close(a.stop)
}

// watchWinch forwards SIGWINCH to the PTY.
func (a *Agentd) watchWinch() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-a.stop:
			return
		case <-sigCh:
			sz, err := pty.GetsizeFull(os.Stdin)
			if err != nil {
				continue
			}
			pty.Setsize(a.ptmx, sz)
		}
	}
}
