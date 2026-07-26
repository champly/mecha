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
// and its input handler is confirmed active.
//
// Phase 1 waits for the initial render to complete (output quiet for
// readyQuietPeriod). Passive timing alone cannot distinguish "TUI waiting for
// input" from "TUI paused between initialization steps" — a quiet window
// during init would close ready too early, and tasks written before the input
// handler is ready land as text in the input box but the enter key is ignored.
//
// Phase 2 eliminates that ambiguity by actively probing: it sends a carriage
// return and verifies the agent produces output in response. A TUI with a
// live input handler will react to the keystroke (cursor move, prompt
// refresh, bell); a TUI still initializing will silently consume it.
func (a *Agentd) watchReady() {
	defer close(a.ready)

	// Phase 1: wait for the initial TUI render to quiet down.
	deadline := time.Now().Add(readyTimeout)
	quiet := false
	for time.Now().Before(deadline) {
		last := a.lastOutput.Load()
		if last != 0 && time.Since(time.Unix(0, last)) > readyQuietPeriod {
			quiet = true
			break
		}
		select {
		case <-a.stop:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !quiet {
		slog.Warn("agent readiness quiet period timed out; tasks will be written anyway", "id", a.opts.ID)
		return
	}

	// Phase 2: actively probe the TUI input handler. Each probe sends a
	// carriage return and waits for the agent to react. Output after the
	// probe confirms the input handler processed the keystroke.
	const maxProbes = 5
	for range maxProbes {
		select {
		case <-a.stop:
			return
		default:
		}

		before := a.lastOutput.Load()

		a.mu.Lock()
		if a.ptmx == nil {
			a.mu.Unlock()
			return
		}
		_, err := io.WriteString(a.ptmx, "\r")
		a.mu.Unlock()
		if err != nil {
			slog.Warn("agent readiness probe write failed", "id", a.opts.ID, "err", err)
			return
		}

		// Wait briefly for the agent to process the keystroke and render.
		select {
		case <-a.stop:
			return
		case <-time.After(200 * time.Millisecond):
		}

		if a.lastOutput.Load() > before {
			return // input handler confirmed alive
		}
	}

	slog.Warn("agent readiness probes produced no reaction; tasks will be written anyway", "id", a.opts.ID)
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
		a.hasTask = false
		a.taskCh <- taskResult{result: "agent exited during task"}
	}
	a.mu.Unlock()

	a.mu.Lock()
	a.ptmx.Close()
	a.ptmx = nil
	a.mu.Unlock()
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
			a.mu.Lock()
			ptmx := a.ptmx
			a.mu.Unlock()
			if ptmx == nil {
				return
			}
			sz, err := pty.GetsizeFull(os.Stdin)
			if err != nil {
				continue
			}
			pty.Setsize(ptmx, sz)
		}
	}
}
