package agentd

import (
	"fmt"
	"io"
	"log/slog"
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

// taskLoop processes tasks received from Core over the stream. Core
// dispatches one task at a time, so tasks are handled serially: PTY writes,
// the single task slot, and stream sends all stay race-free.
func (a *Agentd) taskLoop(stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest]) {
	for {
		req, err := stream.Recv()
		if err != nil {
			// A broken stream means Core is gone; without this the agentd and
			// its agent would run on as orphans (the agent keeps executing but
			// nobody can deliver results). Exiting lets Core's respawn chain
			// recover: a fresh Core spawns a new coordinator, and a specialist
			// is respawned on the next ask.
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
	// Write the task text first, then the enter key after a short delay.
	// Writing both in one string can cause the \r to arrive before the
	// TUI event loop has processed the preceding text, especially for
	// long tasks — the text lands in the input box but the enter key is
	// consumed before the submit handler is primed.
	if _, err := io.WriteString(a.ptmx, req.Task); err != nil {
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

	// Release the task slot no matter how the wait below ends, so a later
	// Stop hook or agent exit can never misdeliver into it.
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
		// No Stop hook ever came: the agent swallowed the task. Exiting
		// closes the stream, which makes Core fail this task and mark the
		// instance unhealthy so the next ask spawns a fresh agentd.
		slog.Error("agentd: task timed out, exiting so Core can respawn", "id", a.opts.ID, "task", req.Id)
		a.signalStop()
	}
}
