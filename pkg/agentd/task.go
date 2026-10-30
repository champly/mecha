package agentd

import (
	"context"
	"fmt"
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
// The session may receive tasks before the TUI is ready and will queue them.
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

// handleTask gives the request to the session actor and waits for a result.
// All PTY writes and submission confirmation happen inside the actor.
func (a *Agentd) handleTask(stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest], req *api.TaskRequest) {
	if a.session == nil {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent session is not running"})
		return
	}
	resultCh, ok := a.session.enqueueTask(req)
	if !ok {
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent session is unavailable"})
		return
	}

	select {
	case r := <-resultCh:
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

const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)
