package core

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/api"
	"google.golang.org/grpc/metadata"
)

// fakeTaskStream implements grpc.BidiStreamingServer[api.TaskResult,
// api.TaskRequest], capturing sent requests.
type fakeTaskStream struct {
	sent chan *api.TaskRequest
}

func (f *fakeTaskStream) Send(req *api.TaskRequest) error {
	f.sent <- req
	return nil
}
func (f *fakeTaskStream) Recv() (*api.TaskResult, error) { return nil, io.EOF }
func (f *fakeTaskStream) SetHeader(metadata.MD) error    { return nil }
func (f *fakeTaskStream) SendHeader(metadata.MD) error   { return nil }
func (f *fakeTaskStream) SetTrailer(metadata.MD)         {}
func (f *fakeTaskStream) Context() context.Context       { return context.Background() }
func (f *fakeTaskStream) SendMsg(any) error              { return nil }
func (f *fakeTaskStream) RecvMsg(any) error              { return io.EOF }

// A result left in the channel by a previous timed-out task must not be
// delivered to the next task.
func TestExecuteDiscardsStaleResult(t *testing.T) {
	inst := newInstance("inst-1", "coder")
	stream := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	inst.attach(stream)

	inst.deliverResult(&api.AskResponse{Id: "stale-id", Success: true, Result: "old"})

	go func() {
		req := <-stream.sent
		inst.deliverResult(&api.AskResponse{Id: req.Id, Success: true, Result: "fresh"})
	}()

	resp, err := inst.execute(context.Background(), "do it")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if resp.GetResult() != "fresh" {
		t.Errorf("execute returned %q, want the fresh result", resp.GetResult())
	}
}

// A stale result that lands after execute's drain but before the fresh one
// occupies the buffer; the fresh result must evict it, not be dropped. With
// a "drop on full" delivery this hangs until the task timeout.
func TestExecuteEvictsStaleResultFromFullBuffer(t *testing.T) {
	inst := newInstance("inst-1", "coder")
	stream := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	inst.attach(stream)

	respCh := make(chan *api.AskResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := inst.execute(context.Background(), "do it")
		respCh <- resp
		errCh <- err
	}()

	req := <-stream.sent // execute has sent the task; its drain already ran
	inst.deliverResult(&api.AskResponse{Id: "stale-" + req.Id, Success: true, Result: "old"})
	inst.deliverResult(&api.AskResponse{Id: req.Id, Success: true, Result: "fresh"})

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if resp := <-respCh; resp.GetResult() != "fresh" {
			t.Errorf("execute returned %q, want the fresh result", resp.GetResult())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execute hung: stale result swallowed the fresh one")
	}
}

// A detached stream must fail the in-flight task immediately instead of
// letting it hang until the task timeout.
func TestDetachFailsInflightTask(t *testing.T) {
	inst := newInstance("inst-1", "coder")
	stream := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	inst.attach(stream)

	errCh := make(chan error, 1)
	go func() {
		_, err := inst.execute(context.Background(), "do it")
		errCh <- err
	}()

	<-stream.sent // the task is on the wire
	inst.detach(stream)

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Errorf("execute error = %v, want a disconnect error", err)
	}
	if got := inst.state.Load(); got != int32(stateUnhealthy) {
		t.Errorf("state = %d, want unhealthy (%d)", got, stateUnhealthy)
	}
}

// A stale stream teardown (agentd reconnected) must not drop the newer
// stream nor mark the instance unhealthy.
func TestDetachForeignStreamIsNoop(t *testing.T) {
	inst := newInstance("inst-1", "coder")
	stale := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	current := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	inst.attach(stale)
	inst.attach(current)

	inst.detach(stale)

	inst.mu.Lock()
	stream := inst.stream
	inst.mu.Unlock()
	if stream != current {
		t.Error("stale detach dropped the current stream")
	}
	if got := inst.state.Load(); got == int32(stateUnhealthy) {
		t.Error("stale detach marked the instance unhealthy")
	}
}

// Late results arriving when no execute is waiting (timed-out or cancelled
// tasks) must be dropped rather than block the TaskChannel handler once the
// buffer fills.
func TestDeliverResultNeverBlocks(t *testing.T) {
	inst := newInstance("inst-1", "coder")
	stream := &fakeTaskStream{sent: make(chan *api.TaskRequest, 1)}
	inst.attach(stream)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// One more than the buffer holds, with no execute draining.
		for i := 0; i < 3; i++ {
			inst.deliverResult(&api.AskResponse{Id: "late", Success: true})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliverResult blocked with no execute waiting")
	}
}
