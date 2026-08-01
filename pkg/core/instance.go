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
	discCh      chan struct{} // closed by detach when the stream breaks
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
	inst.discCh = make(chan struct{})
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

// markExited marks the agent as exited, fails any in-flight task via discCh,
// and makes the instance unhealthy. The discCh close is what unblocks execute
// immediately; relying on the agentd's later connection close would leave the
// task hanging until the timeout.
func (inst *instance) markExited() {
	inst.closeDisc()
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
	stream, resultCh, discCh := inst.stream, inst.resultCh, inst.discCh
	inst.mu.Unlock()
	if stream == nil || resultCh == nil || discCh == nil {
		return nil, fmt.Errorf("core: instance %q task channel not ready", inst.id)
	}

	inst.state.Store(int32(stateBusy))
	timedOut := false
	defer func() {
		if timedOut {
			// The agent never answered; keep the instance unhealthy so the
			// next ask destroys it and respawns a fresh agentd. The stuck
			// agent would otherwise consume every subsequent timeout too.
			inst.state.Store(int32(stateUnhealthy))
			return
		}
		inst.state.CompareAndSwap(int32(stateBusy), int32(stateRunning))
	}()

	// Drain stale results from a previous timed-out or cancelled task, or the
	// buffer could swallow this task's result below. taskMu serializes
	// executes, so no execute is waiting on these entries.
drain:
	for {
		select {
		case <-resultCh:
		default:
			break drain
		}
	}

	id := uuid.NewString()
	if err := stream.Send(&api.TaskRequest{Id: id, Task: task}); err != nil {
		return nil, fmt.Errorf("core: send task: %w", err)
	}

	// One timer for the whole wait: discarding stale results must not extend
	// the deadline.
	timer := time.NewTimer(api.TaskTimeout)
	defer timer.Stop()
	for {
		select {
		case result := <-resultCh:
			if result.GetId() != id {
				// Stale result from a previous timed-out or cancelled
				// task; keep waiting for ours.
				continue
			}
			return result, nil
		case <-discCh:
			return nil, fmt.Errorf("core: instance %q agentd disconnected during task", inst.id)
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			timedOut = true
			return nil, fmt.Errorf("core: task timeout")
		}
	}
}

// deliverResult hands a task result to the waiting execute.
func (inst *instance) deliverResult(resp *api.AskResponse) {
	inst.mu.Lock()
	resultCh := inst.resultCh
	inst.mu.Unlock()

	if resultCh == nil {
		return
	}
	// Never block: when no execute is waiting (its task timed out or was
	// cancelled), a late result must not wedge the TaskChannel handler once
	// the buffer fills. Results arrive in task order (agentd handles tasks
	// serially), so a full buffer can only hold a stale result from an older
	// task — evict it for the fresh one, which is what execute is waiting on.
	// Dropping the fresh result here would hang execute for the whole
	// TaskTimeout.
	select {
	case resultCh <- resp:
	default:
		select {
		case <-resultCh:
		default:
		}
		select {
		case resultCh <- resp:
		default:
		}
	}
}

// detach drops the stream, fails any in-flight task via discCh, and marks the
// instance unhealthy so the next ask respawns it. It is a no-op when a newer
// stream has been attached since (agentd reconnected), so a stale stream
// teardown cannot detach its successor.
func (inst *instance) detach(stream grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]) {
	inst.mu.Lock()
	if inst.stream != stream {
		inst.mu.Unlock()
		return
	}
	inst.stream = nil
	inst.resultCh = nil
	inst.mu.Unlock()

	inst.closeDisc()
	inst.state.Store(int32(stateUnhealthy))
}

// closeDisc closes and clears discCh exactly once.
func (inst *instance) closeDisc() {
	inst.mu.Lock()
	discCh := inst.discCh
	inst.discCh = nil
	inst.mu.Unlock()
	if discCh != nil {
		close(discCh)
	}
}
