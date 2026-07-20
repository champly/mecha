package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/term"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type instanceState int32

const (
	stateStarting  instanceState = iota + 1 // spawned, waiting for register and agent start
	stateRunning                            // ready for tasks
	stateBusy                               // task in flight
	stateUnhealthy                          // agent exited, respawn on next ask
)

// instance is Core's view of one agentd connection: registration, task
// stream, readiness, and task execution.
type instance struct {
	id   string
	role string

	state atomic.Int32 // instanceState

	taskMu sync.Mutex // serializes tasks

	mu          sync.Mutex
	handle      term.Handle // specialist pane; nil for the coordinator
	stream      grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]
	resultCh    chan *api.AskResponse
	streamUp    bool
	agentUp     bool
	registered  bool
	registerCh  chan struct{}
	readyClosed bool
	readyCh     chan struct{}
}

func newInstance(id, role string) *instance {
	inst := &instance{
		id:         id,
		role:       role,
		registerCh: make(chan struct{}),
		readyCh:    make(chan struct{}),
	}
	inst.state.Store(int32(stateStarting))
	return inst
}

func (inst *instance) setHandle(h term.Handle) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.handle = h
}

// pane returns the pane handle (nil for the coordinator).
func (inst *instance) pane() term.Handle {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.handle
}

// markRegistered marks the agentd as registered.
func (inst *instance) markRegistered() {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if !inst.registered {
		inst.registered = true
		close(inst.registerCh)
	}
}

// attach mounts the TaskChannel stream.
func (inst *instance) attach(stream grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]) {
	inst.mu.Lock()
	inst.stream = stream
	inst.resultCh = make(chan *api.AskResponse, 1)
	inst.streamUp = true
	inst.mu.Unlock()
	inst.maybeReady()
}

// markStarted marks the agent as started (SessionStart hook received).
func (inst *instance) markStarted() {
	inst.mu.Lock()
	inst.agentUp = true
	inst.mu.Unlock()
	inst.maybeReady()
	inst.state.Store(int32(stateRunning))
}

// markExited marks the agent as exited; the instance becomes unhealthy.
func (inst *instance) markExited() {
	inst.state.Store(int32(stateUnhealthy))
}

// maybeReady closes readyCh once both the stream and the agent are up,
// regardless of arrival order.
func (inst *instance) maybeReady() {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.streamUp && inst.agentUp && !inst.readyClosed {
		inst.readyClosed = true
		close(inst.readyCh)
	}
}

// waitRegistered blocks until the agentd registers or times out.
func (inst *instance) waitRegistered(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	select {
	case <-inst.registerCh:
		return nil
	case <-tctx.Done():
		return fmt.Errorf("core: instance %q register timeout", inst.id)
	}
}

// waitReady blocks until the instance can accept tasks (stream attached and
// agent started) or times out.
func (inst *instance) waitReady(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, agentStartTimeout)
	defer cancel()

	select {
	case <-inst.readyCh:
		return nil
	case <-tctx.Done():
		return fmt.Errorf("core: instance %q agent start timeout", inst.id)
	}
}

// execute sends one task at a time and blocks for its result.
func (inst *instance) execute(ctx context.Context, task string) (*api.AskResponse, error) {
	inst.taskMu.Lock()
	defer inst.taskMu.Unlock()

	inst.mu.Lock()
	stream, resultCh := inst.stream, inst.resultCh
	inst.mu.Unlock()
	if stream == nil || resultCh == nil {
		return nil, fmt.Errorf("core: instance %q task channel not ready", inst.id)
	}

	inst.state.Store(int32(stateBusy))
	defer inst.state.CompareAndSwap(int32(stateBusy), int32(stateRunning))

	if err := stream.Send(&api.TaskRequest{Id: uuid.NewString(), Task: task}); err != nil {
		return nil, fmt.Errorf("core: send task: %w", err)
	}

	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(taskTimeout):
		return nil, fmt.Errorf("core: task timeout")
	}
}

// deliverResult hands a task result to the waiting execute.
func (inst *instance) deliverResult(resp *api.AskResponse) {
	inst.mu.Lock()
	resultCh := inst.resultCh
	inst.mu.Unlock()

	if resultCh != nil {
		// Serialized tasks guarantee the channel never overflows.
		resultCh <- resp
	}
}

// detach drops the stream and fails any in-flight task.
func (inst *instance) detach() {
	inst.mu.Lock()
	resultCh := inst.resultCh
	inst.stream = nil
	inst.resultCh = nil
	inst.mu.Unlock()

	if resultCh != nil {
		select {
		case resultCh <- &api.AskResponse{Success: false, Result: "agentd disconnected"}:
		default:
		}
	}
}
