package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
)

// ---------------------------------------------------------------------------
// mock backend
// ---------------------------------------------------------------------------

type mockBackend struct {
	spawned  []term.Spec
	sent     map[string][]string
	killed   map[string]bool
	sendErr  error // if non-nil, Send returns this error
	sendHook func(term.Handle, string)
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		sent:   make(map[string][]string),
		killed: make(map[string]bool),
	}
}

func (m *mockBackend) Spawn(_ context.Context, spec term.Spec) (term.Handle, error) {
	m.spawned = append(m.spawned, spec)
	return mockHandle{id: spec.WorkDir}, nil
}

func (m *mockBackend) Send(_ context.Context, handle term.Handle, text string) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	if m.sendHook != nil {
		m.sendHook(handle, text)
	}
	m.sent[handle.ID()] = append(m.sent[handle.ID()], text)
	return nil
}

func (m *mockBackend) Capture(_ context.Context, _ term.Handle) (string, error)    { return "", nil }
func (m *mockBackend) CaptureAll(_ context.Context, _ term.Handle) (string, error) { return "", nil }

func (m *mockBackend) Kill(_ context.Context, handle term.Handle) error {
	m.killed[handle.ID()] = true
	return nil
}

type mockHandle struct{ id string }

func (h mockHandle) ID() string     { return h.id }
func (h mockHandle) PaneID() string { return h.id }

// ---------------------------------------------------------------------------
// mock agent
// ---------------------------------------------------------------------------

type mockAgent struct{ id string }

func (a *mockAgent) Prepare() error { return nil }
func (a *mockAgent) Cmd() *exec.Cmd { return exec.Command("echo") }
func (a *mockAgent) ID() string     { return a.id }
func (a *mockAgent) ParseHookEvent(raw []byte) (types.HookEvent, error) {
	var m map[string]string
	json.Unmarshal(raw, &m)
	return types.HookEvent{
		AgentID: a.id,
		Event:   m["event"],
	}, nil
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func testCore(t *testing.T) (*Core, *mockBackend) {
	t.Helper()
	backend := newMockBackend()
	c := &Core{
		workspace:         t.TempDir(),
		cfg:               config.Config{Profile: "test", Profiles: map[string]config.ProfileConfig{"test": {}}},
		backend:           backend,
		specialists:       make(map[string]*instance),
		agentByID:         make(map[string]types.Agent),
		instanceByAgentID: make(map[string]*instance),
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		logFile:           nil,
	}
	return c, backend
}

func addAgent(c *Core, agentID string, status int32) *instance {
	a := &mockAgent{id: agentID}
	inst := newInstance("test-role")
	inst.agent = a
	inst.handle = mockHandle{id: agentID + "-handle"}
	inst.status.Store(status)
	if status == statusStarting {
		inst.ready = make(chan struct{})
		inst.readyOnce = sync.Once{}
	}
	c.agentByID[agentID] = a
	c.instanceByAgentID[agentID] = inst
	c.specialists["test-role"] = inst
	return inst
}

// ---------------------------------------------------------------------------
// onEvent: SessionStart
// ---------------------------------------------------------------------------

func TestOnEvent_SessionStart_Starting(t *testing.T) {
	c, _ := testCore(t)
	inst := addAgent(c, "agent-1", statusStarting)

	c.onEvent("agent-1", types.HookEvent{Event: types.EventSessionStart})

	select {
	case <-inst.ready:
	default:
		t.Error("ready should be closed on SessionStart when status is starting")
	}
}

func TestOnEvent_SessionStart_Running(t *testing.T) {
	c, _ := testCore(t)
	inst := addAgent(c, "agent-1", statusRunning)

	c.onEvent("agent-1", types.HookEvent{Event: types.EventSessionStart})

	if inst.status.Load() != statusRunning {
		t.Errorf("status should stay running")
	}
}

func TestOnEvent_SessionStart_Busy(t *testing.T) {
	c, _ := testCore(t)
	inst := addAgent(c, "agent-1", statusBusy)

	c.onEvent("agent-1", types.HookEvent{Event: types.EventSessionStart})

	if inst.status.Load() != statusBusy {
		t.Errorf("status should stay busy")
	}
}

// ---------------------------------------------------------------------------
// onEvent: Stop
// ---------------------------------------------------------------------------

func TestOnEvent_Stop_Busy(t *testing.T) {
	c, _ := testCore(t)
	inst := addAgent(c, "agent-1", statusBusy)
	ch := make(chan taskResult, 1)
	inst.result.Store(&ch)

	c.onEvent("agent-1", types.HookEvent{Event: types.EventStop, Output: "task done"})

	select {
	case r := <-ch:
		if r.output != "task done" {
			t.Errorf("output = %q, want %q", r.output, "task done")
		}
	default:
		t.Error("result should be sent on Stop when busy")
	}
}

func TestOnEvent_Stop_NotBusy(t *testing.T) {
	c, _ := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	// should not panic when no result chan
	c.onEvent("agent-1", types.HookEvent{Event: types.EventStop, Output: "done"})
}

// ---------------------------------------------------------------------------
// onEvent: StopFailure
// ---------------------------------------------------------------------------

func TestOnEvent_StopFailure_Busy(t *testing.T) {
	c, _ := testCore(t)
	inst := addAgent(c, "agent-1", statusBusy)
	ch := make(chan taskResult, 1)
	inst.result.Store(&ch)

	c.onEvent("agent-1", types.HookEvent{Event: types.EventStopFailure, Error: "rate_limit"})

	select {
	case r := <-ch:
		if r.err != "rate_limit" {
			t.Errorf("error = %q, want %q", r.err, "rate_limit")
		}
	default:
		t.Error("result should be sent on StopFailure when busy")
	}
}

// ---------------------------------------------------------------------------
// onEvent: unknown agent
// ---------------------------------------------------------------------------

func TestOnEvent_UnknownAgent(t *testing.T) {
	c, _ := testCore(t)
	// should not panic
	c.onEvent("nonexistent", types.HookEvent{Event: types.EventStop})
}

// ---------------------------------------------------------------------------
// Ask
// ---------------------------------------------------------------------------

func TestAsk_Success(t *testing.T) {
	c, backend := testCore(t)
	inst := addAgent(c, "agent-1", statusRunning)

	go func() {
		time.Sleep(10 * time.Millisecond)
		c.onEvent("agent-1", types.HookEvent{Event: types.EventStop, Output: "task output"})
	}()

	result, err := c.Ask(context.Background(), "test-role", "do something")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.output != "task output" {
		t.Errorf("output = %q, want %q", result.output, "task output")
	}
	if inst.status.Load() != statusRunning {
		t.Errorf("status should be running after completion")
	}

	sent := backend.sent["agent-1-handle"]
	if len(sent) != 1 || sent[0] != "do something\n" {
		t.Errorf("task should be sent, got %v", sent)
	}
}

func TestAsk_Failure(t *testing.T) {
	c, _ := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	go func() {
		time.Sleep(10 * time.Millisecond)
		c.onEvent("agent-1", types.HookEvent{Event: types.EventStopFailure, Error: "overloaded"})
	}()

	result, err := c.Ask(context.Background(), "test-role", "do something")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.err != "overloaded" {
		t.Errorf("error = %q, want %q", result.err, "overloaded")
	}
}

func TestAsk_EmptyTask(t *testing.T) {
	c, _ := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	result, err := c.Ask(context.Background(), "test-role", "")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.output != "" || result.err != "" {
		t.Errorf("expected empty result, got %+v", result)
	}
}

func TestAsk_ContextCanceled(t *testing.T) {
	c, _ := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Ask(ctx, "test-role", "task")
	if err == nil {
		t.Fatal("expected context canceled error")
	}
}

func TestAsk_SendErrorResetsStatus(t *testing.T) {
	c, backend := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	// Make Send return an error to verify status is reset.
	backend.sendErr = fmt.Errorf("send failed")

	_, err := c.Ask(context.Background(), "test-role", "do something")
	if err == nil {
		t.Fatal("expected send error")
	}
	if err.Error() == "" || !strings.Contains(err.Error(), "send failed") {
		t.Errorf("expected send failed error, got: %v", err)
	}

	inst := c.specialists["test-role"]
	if inst == nil {
		t.Fatal("instance should still exist")
	}
	if inst.status.Load() != statusRunning {
		t.Errorf("status should be running after send error")
	}
}

func TestAsk_SerializesConcurrentTasks(t *testing.T) {
	c, backend := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	sendStarted := make(chan string, 2)
	var sendCount atomic.Int32
	backend.sendHook = func(_ term.Handle, text string) {
		sendCount.Add(1)
		sendStarted <- text
	}

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	var firstResult taskResult
	var secondResult taskResult
	var firstErr error
	var secondErr error

	go func() {
		firstResult, firstErr = c.Ask(context.Background(), "test-role", "first task")
		close(firstDone)
	}()

	select {
	case got := <-sendStarted:
		if got != "first task\n" {
			t.Fatalf("first send = %q, want %q", got, "first task\\n")
		}
	case <-time.After(time.Second):
		t.Fatal("first task was not sent")
	}

	go func() {
		secondResult, secondErr = c.Ask(context.Background(), "test-role", "second task")
		close(secondDone)
	}()

	select {
	case got := <-sendStarted:
		t.Fatalf("second task sent before first completed: %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	c.onEvent("agent-1", types.HookEvent{Event: types.EventStop, Output: "first output"})

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first Ask did not complete")
	}

	if firstErr != nil {
		t.Fatalf("first Ask error: %v", firstErr)
	}
	if firstResult.output != "first output" {
		t.Fatalf("first output = %q, want %q", firstResult.output, "first output")
	}

	select {
	case got := <-sendStarted:
		if got != "second task\n" {
			t.Fatalf("second send = %q, want %q", got, "second task\\n")
		}
	case <-time.After(time.Second):
		t.Fatal("second task was not sent after first completion")
	}

	c.onEvent("agent-1", types.HookEvent{Event: types.EventStop, Output: "second output"})

	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second Ask did not complete")
	}

	if secondErr != nil {
		t.Fatalf("second Ask error: %v", secondErr)
	}
	if secondResult.output != "second output" {
		t.Fatalf("second output = %q, want %q", secondResult.output, "second output")
	}
	if sendCount.Load() != 2 {
		t.Fatalf("send count = %d, want 2", sendCount.Load())
	}
	if c.specialists["test-role"].status.Load() != statusRunning {
		t.Fatalf("status should return to running after serialized tasks")
	}
	if c.specialists["test-role"].result.Load() != nil {
		t.Fatal("result channel should be cleared after serialized tasks")
	}
}

// ---------------------------------------------------------------------------
// ensureSpecialist: reuse
// ---------------------------------------------------------------------------

func TestEnsureSpecialist_Reuse(t *testing.T) {
	c, backend := testCore(t)
	addAgent(c, "agent-1", statusRunning)

	inst, err := c.ensureSpecialist(context.Background(), "test-role")
	if err != nil {
		t.Fatalf("ensureSpecialist: %v", err)
	}
	if inst.status.Load() != statusRunning {
		t.Errorf("status should be running")
	}
	// No new spawn
	if len(backend.spawned) != 0 {
		t.Errorf("expected no new spawn, got %d", len(backend.spawned))
	}
}

// ---------------------------------------------------------------------------
// coordinator cleanup
// ---------------------------------------------------------------------------

func TestLaunchCoordinator_Cleanup(t *testing.T) {
	c, backend := testCore(t)

	// Add a busy specialist with a pending result channel.
	busyInst := addAgent(c, "agent-busy", statusBusy)
	ch := make(chan taskResult, 1)
	busyInst.result.Store(&ch)

	// Add an idle (running) specialist -- should not receive a shutdown notification.
	idleRole := "idle-role"
	idleAgent := &mockAgent{id: "agent-idle"}
	idleInst := &instance{
		role:   idleRole,
		agent:  idleAgent,
		handle: mockHandle{id: "agent-idle-handle"},
	}
	idleInst.status.Store(statusRunning)
	c.agentByID["agent-idle"] = idleAgent
	c.instanceByAgentID["agent-idle"] = idleInst
	c.specialists[idleRole] = idleInst

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Act: run the real cleanup sequence used by launchCoordinator.
	c.drainSpecialists(ctx, 0) // 0 grace period = sends notification but does not sleep
	c.forceCleanup(ctx)

	// Assert: both maps are cleared.
	if len(c.specialists) != 0 {
		t.Errorf("specialists should be empty after forceCleanup, got %d", len(c.specialists))
	}
	if len(c.instanceByAgentID) != 0 {
		t.Errorf("instanceByAgentID should be empty after forceCleanup, got %d", len(c.instanceByAgentID))
	}
	if len(c.agentByID) != 0 {
		t.Errorf("agentByID should be empty after forceCleanup, got %d", len(c.agentByID))
	}

	// Assert: both instances were killed.
	if !backend.killed["agent-busy-handle"] {
		t.Error("busy specialist should be killed")
	}
	if !backend.killed["agent-idle-handle"] {
		t.Error("idle specialist should be killed")
	}

	// Assert: shutdown notification was sent only to the busy specialist.
	busySent := backend.sent["agent-busy-handle"]
	if len(busySent) != 1 {
		t.Fatalf("busy specialist should have 1 shutdown notification, got %d", len(busySent))
	}
	expectedMsg := "[SYSTEM] The coordinator has exited. The session will be terminated shortly."
	if busySent[0] != expectedMsg {
		t.Errorf("shutdown notification mismatch:\ngot:  %q\nwant: %q", busySent[0], expectedMsg)
	}

	idleSent := backend.sent["agent-idle-handle"]
	if len(idleSent) != 0 {
		t.Errorf("idle specialist should not receive shutdown notification, got %d messages", len(idleSent))
	}

	// Assert: busy specialist's result channel received the cleanup error.
	select {
	case r := <-ch:
		if r.err != "coordinator exited" {
			t.Errorf("result error = %q, want %q", r.err, "coordinator exited")
		}
	default:
		t.Error("result should be sent on cleanup")
	}
}
