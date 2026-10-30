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

	mu            sync.Mutex
	handle        term.Handle // specialist pane; nil for the coordinator
	stream        grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]
	resultCh      chan *api.AskResponse
	discCh        chan struct{} // closed by detach when the stream breaks
	streamUp      bool
	agentUp       bool
	registered    bool
	registerCh    chan struct{}
	readyClosed   bool
	readyCh       chan struct{}
	failedCh      chan struct{}
	failedClosed  bool
	failureReason string
	exitedCh      chan struct{}
	exitedClosed  bool
}

func newInstance(id, role string) *instance {
	inst := &instance{
		id:         id,
		role:       role,
		registerCh: make(chan struct{}),
		readyCh:    make(chan struct{}),
		failedCh:   make(chan struct{}),
		exitedCh:   make(chan struct{}),
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

// markStarted marks the agent process as started. It returns true only when
// this call advances a starting instance to running.
func (inst *instance) markStarted() bool {
	inst.mu.Lock()
	inst.agentUp = true
	inst.mu.Unlock()
	inst.maybeReady()
	// Only a starting instance becomes running: a duplicate SessionStart must
	// not clobber busy or revive a dead agent.
	return inst.state.CompareAndSwap(int32(stateStarting), int32(stateRunning))
}

// markExited makes the instance unhealthy and unblocks an in-flight execute
// through discCh, rather than waiting for the agentd connection to close.
func (inst *instance) markExited() {
	inst.state.Store(int32(stateUnhealthy))
	inst.closeDisc()
	inst.closeExited()
}

// markFailed records a startup failure and wakes waitReady with its reason.
func (inst *instance) markFailed(reason string) {
	inst.state.Store(int32(stateUnhealthy))
	inst.mu.Lock()
	if inst.failureReason == "" {
		inst.failureReason = reason
	}
	if !inst.failedClosed {
		inst.failedClosed = true
		close(inst.failedCh)
	}
	inst.mu.Unlock()
	inst.closeDisc()
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
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("core: instance %q register timeout", inst.id)
	}
}

// waitReady blocks until the instance can accept tasks (stream attached and
// agent started) or times out.
func (inst *instance) waitReady(ctx context.Context) error {
	return inst.waitReadyWithin(ctx, agentStartTimeout)
}

// waitReadyWithin is waitReady with an explicit readiness budget. A timeout
// <= 0 waits until ready, failure, exit, or context cancellation.
func (inst *instance) waitReadyWithin(ctx context.Context, timeout time.Duration) error {
	tctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	select {
	case <-inst.readyCh:
		if inst.state.Load() == int32(stateUnhealthy) {
			if reason := inst.startupFailureReason(); reason != "" {
				return fmt.Errorf("core: instance %q failed to start: %s", inst.id, reason)
			}
			return fmt.Errorf("core: instance %q agent exited before ready", inst.id)
		}
		return nil
	case <-inst.failedCh:
		reason := inst.startupFailureReason()
		if reason == "" {
			reason = "unknown startup failure"
		}
		return fmt.Errorf("core: instance %q failed to start: %s", inst.id, reason)
	case <-inst.exitedCh:
		return fmt.Errorf("core: instance %q agent exited before ready", inst.id)
	case <-tctx.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("core: instance %q agent start timeout", inst.id)
	}
}

func (inst *instance) startupFailureReason() string {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.failureReason
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
			// next ask respawns it instead of reusing a stuck agent.
			inst.state.Store(int32(stateUnhealthy))
			return
		}
		inst.state.CompareAndSwap(int32(stateBusy), int32(stateRunning))
	}()

	// Drop stale results from an earlier timed-out task, or the buffered
	// channel could swallow this task's result. taskMu serializes executes,
	// so no one else is waiting on these entries.
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

	// One timer for the whole wait: skipping stale results must not extend it.
	timer := time.NewTimer(api.TaskTimeout)
	defer timer.Stop()
	for {
		select {
		case result := <-resultCh:
			if result.GetId() != id {
				// Stale result from an earlier task; keep waiting.
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
	// Never block when no execute is waiting, or a late result would wedge the
	// TaskChannel handler once the buffer fills. Results arrive in order, so a
	// full buffer only holds a stale result: evict it for the fresh one.
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

// detach drops the stream, unblocks an in-flight execute, and marks the
// instance unhealthy so the next ask respawns it. A stream that has already
// been replaced by a reconnect is left alone.
func (inst *instance) detach(stream grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]) {
	inst.mu.Lock()
	if inst.stream != stream {
		inst.mu.Unlock()
		return
	}
	inst.stream = nil
	inst.resultCh = nil
	inst.mu.Unlock()

	inst.state.Store(int32(stateUnhealthy))
	inst.closeDisc()
	inst.closeExited()
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

// closeExited wakes callers waiting for startup after the agent exits.
func (inst *instance) closeExited() {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.exitedClosed {
		return
	}
	inst.exitedClosed = true
	close(inst.exitedCh)
}
