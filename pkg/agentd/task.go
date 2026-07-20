package agentd

import (
	"fmt"
	"io"

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

// taskLoop processes tasks received from Core over the stream.
func (a *Agentd) taskLoop(stream grpc.BidiStreamingClient[api.TaskResult, api.TaskRequest]) {
	for {
		req, err := stream.Recv()
		if err != nil {
			return
		}
		go a.handleTask(stream, req)
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
	if _, err := io.WriteString(a.ptmx, req.Task+"\r"); err != nil {
		a.mu.Unlock()
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "write to pty: " + err.Error()})
		return
	}
	a.hasTask = true
	a.mu.Unlock()

	select {
	case r := <-a.taskCh:
		_ = stream.Send(&api.TaskResult{Id: req.Id, Success: r.success, Result: r.result})
	case <-a.stop:
		_ = stream.Send(&api.TaskResult{Id: req.Id, Result: "agent exited during task"})
	}
}
