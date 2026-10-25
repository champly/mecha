package agentd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/champly/mecha/pkg/api"
	"google.golang.org/grpc"
)

// taskResult holds the result of a task execution.
type taskResult struct {
	success bool
	result  string
}

// connectTaskChannel opens the TaskChannel stream and starts the task loop.
// It runs before the agent starts so Core can dispatch tasks as soon as the
// instance reports ready.
//
// Stream creation is bounded by startupTimeout so a hung Core cannot wedge
// SIGTERM handling. The stream itself uses a separate context (canceled by
// Close), because a startup timeout must not kill a long-running task.
func (a *Agentd) connectTaskChannel() error {
	streamCtx, streamCancel := context.WithCancel(a.ctx())
	type result struct {
		stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest]
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		stream, err := a.client.TaskChannel(streamCtx)
		ch <- result{stream: stream, err: err}
	}()

	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		if r.err != nil {
			streamCancel()
			return fmt.Errorf("agentd: open task channel: %w", r.err)
		}
		a.mu.Lock()
		a.streamCancel = streamCancel
		a.mu.Unlock()
		go a.taskLoop(r.stream)
		return nil
	case <-timer.C:
		streamCancel()
		return fmt.Errorf("agentd: open task channel: timed out after %s", startupTimeout)
	}
}

// taskLoop processes tasks from Core serially, so PTY writes, the single task
// slot, and stream sends all stay race-free.
func (a *Agentd) taskLoop(stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest]) {
	for {
		req, err := stream.Recv()
		if err != nil {
			// A broken stream means Core is gone; exit so this agentd does not
			// linger as an orphan and Core can respawn on the next ask.
			slog.Warn("agentd: task channel closed, exiting", "id", a.opts.ID, "err", err)
			a.signalStop()
			return
		}
		a.handleTask(stream, req)
	}
}

// handleTask writes the task to the agent PTY and waits for the hook-driven result.
func (a *Agentd) handleTask(stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest], req *api.TaskRequest) {
	// Wait for the TUI to finish initializing; earlier writes lose the enter key.
	select {
	case <-a.ready:
	case <-a.stop:
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited during task"})
		return
	}

	// Never hold a.mu across PTY I/O. stopAgent needs a.mu to learn the agent
	// PID and close the PTY, so a write that blocks because the agent stopped
	// consuming output would otherwise prevent shutdown and let Core SIGKILL
	// agentd while the independent-session agent is still alive.
	a.mu.Lock()
	ptmx := a.ptmx
	a.mu.Unlock()
	if ptmx == nil {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent not running"})
		return
	}
	// Write the text and the enter key separately. Codex needs explicit paste
	// boundaries for long tasks: otherwise its paste-burst detector may still
	// be consuming the text when the enter key arrives and discard that key.
	payload := taskInput(a.agentType, normalizeTask(req.Task))
	if _, err := a.writePTY(ptmx, payload); err != nil {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "write to pty: " + err.Error()})
		return
	}

	// Give the TUI time to ingest the paste before sending the enter key.
	time.Sleep(200 * time.Millisecond)

	// Shutdown may have closed the PTY while the paste was in flight; re-read
	// the pointer instead of using the one captured before the sleep.
	a.mu.Lock()
	ptmx = a.ptmx
	a.mu.Unlock()
	if ptmx == nil {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited before enter key"})
		return
	}
	if _, err := a.writePTY(ptmx, "\r"); err != nil {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "write enter to pty: " + err.Error()})
		return
	}

	// Re-check under the lock that shutdown did not clear the PTY while the
	// enter write was in flight before claiming the task slot.
	a.mu.Lock()
	valid := a.ptmx == ptmx
	if valid {
		a.hasTask = true
	}
	a.mu.Unlock()
	if !valid {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited before enter key"})
		return
	}

	// Always release the task slot, so a late hook cannot misdeliver into it.
	defer func() {
		a.mu.Lock()
		a.hasTask = false
		a.mu.Unlock()
	}()

	select {
	case r := <-a.taskCh:
		_ = stream.Send(&api.TaskResult{Id: req.Id, Success: r.success, Result: r.result})
	case <-a.stop:
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited during task"})
	case <-time.After(api.TaskTimeout):
		// No Stop hook arrived: exit so Core fails this task and respawns
		// the agent on the next ask.
		slog.Error("agentd: task timed out, exiting so Core can respawn", "id", a.opts.ID, "task", req.Id)
		a.signalStop()
	}
}

// writePTY writes to the agent PTY. Tests override ptyWrite to observe the
// point where handleTask enters an I/O that may block; production always
// writes to the real *os.File. *os.File permits concurrent Close/Write, so a
// shutdown that closes the PTY while this write is in flight makes it return
// an error rather than causing a use-after-free.
func (a *Agentd) writePTY(ptmx *os.File, s string) (int, error) {
	if a.ptyWrite != nil {
		return a.ptyWrite(ptmx, s)
	}
	return io.WriteString(ptmx, s)
}

const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)

// taskInput wraps Codex task text in bracketed-paste markers. The markers make
// the paste boundary explicit, so a following Enter is never interpreted as
// part of the pasted input.
func taskInput(agentType, task string) string {
	if agentType == "codex" {
		return bracketedPasteStart + task + bracketedPasteEnd
	}
	return task
}

// normalizeTask collapses the task onto a single line. The task is typed as
// keystrokes, so an embedded newline would submit it early.
func normalizeTask(task string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(task))
}
