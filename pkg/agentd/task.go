package agentd

import (
	"fmt"
	"io"
	"log/slog"
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
func (a *Agentd) connectTaskChannel() error {
	stream, err := a.client.TaskChannel(a.ctx())
	if err != nil {
		return fmt.Errorf("agentd: open task channel: %w", err)
	}
	go a.taskLoop(stream)
	return nil
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

	a.mu.Lock()
	if a.ptmx == nil {
		a.mu.Unlock()
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent not running"})
		return
	}
	// Write the text and the enter key separately: sent as one string, the \r
	// can reach the TUI before it has processed the text, so long tasks lose
	// the enter key and stay in the input box.
	if _, err := io.WriteString(a.ptmx, normalizeTask(req.Task)); err != nil {
		a.mu.Unlock()
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "write to pty: " + err.Error()})
		return
	}
	a.mu.Unlock()

	// Give the TUI time to ingest the text before sending the enter key.
	time.Sleep(100 * time.Millisecond)

	a.mu.Lock()
	if a.ptmx == nil {
		a.mu.Unlock()
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited before enter key"})
		return
	}
	if _, err := io.WriteString(a.ptmx, "\r"); err != nil {
		a.mu.Unlock()
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "write enter to pty: " + err.Error()})
		return
	}
	a.hasTask = true
	a.mu.Unlock()

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

// normalizeTask collapses the task onto a single line. The task is typed as
// keystrokes, so an embedded newline would submit it early.
func normalizeTask(task string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(task))
}
