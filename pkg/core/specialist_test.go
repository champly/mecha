package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeHandle struct{ id, pane string }

func (h fakeHandle) ID() string     { return h.id }
func (h fakeHandle) PaneID() string { return h.pane }

// fakeBackend stands in for a terminal backend. Spawn simulates an agentd that
// registers, attaches its task stream, and reports the agent started before
// returning, so Core's waitRegistered/waitReady succeed immediately.
type fakeBackend struct {
	core *Core

	mu     sync.Mutex
	spawns map[string]int
	killed []string
	seq    int
}

func (f *fakeBackend) Spawn(_ context.Context, spec term.Spec) (term.Handle, error) {
	f.mu.Lock()
	if f.spawns == nil {
		f.spawns = make(map[string]int)
	}
	f.spawns[spec.Role]++
	f.seq++
	n := f.seq
	f.mu.Unlock()

	inst := f.core.registry.getByRole(spec.Role)
	if inst == nil {
		return nil, fmt.Errorf("fakeBackend: no registered instance for role %q", spec.Role)
	}
	inst.markRegistered()
	inst.attach(&fakeTaskStream{sent: make(chan *api.TaskRequest, 1)})
	inst.markStarted()

	return fakeHandle{id: fmt.Sprintf("h-%d", n), pane: fmt.Sprintf("pane-%d", n)}, nil
}

func (f *fakeBackend) Kill(_ context.Context, h term.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, h.PaneID())
	return nil
}

func (f *fakeBackend) Close() error         { return nil }
func (f *fakeBackend) Label(_ string) error { return nil }

func (f *fakeBackend) spawnCount(role string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawns[role]
}

func (f *fakeBackend) killedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// newTestCore builds a Core wired to a fake backend, with a profile that has a
// coordinator plus three specialist roles.
func newTestCore(t *testing.T) *Core {
	t.Helper()
	cfg := config.Config{
		Profile: "test",
		Profiles: map[string]config.ProfileConfig{
			"test": {Roles: []config.Role{
				{Name: "lead", IsCoordinator: true},
				{Name: "coder"},
				{Name: "tester"},
				{Name: "reviewer"},
			}},
		},
	}
	c := &Core{
		cfg:         cfg,
		workspace:   t.TempDir(),
		logger:      slog.New(slog.DiscardHandler),
		mechaBinary: "/bin/mecha",
		registry:    newRegistry(),
		spawnLocks:  make(map[string]*sync.Mutex),
	}
	c.backend = &fakeBackend{core: c}
	return c
}

func TestEnsureSpecialistSpawnsThenReuses(t *testing.T) {
	c := newTestCore(t)
	fb := c.backend.(*fakeBackend)

	inst1, err := c.ensureSpecialist(context.Background(), "coder")
	if err != nil {
		t.Fatalf("first ensureSpecialist: %v", err)
	}
	if got := fb.spawnCount("coder"); got != 1 {
		t.Fatalf("spawn count = %d, want 1", got)
	}

	inst2, err := c.ensureSpecialist(context.Background(), "coder")
	if err != nil {
		t.Fatalf("second ensureSpecialist: %v", err)
	}
	if inst2 != inst1 {
		t.Error("a healthy instance was not reused")
	}
	if got := fb.spawnCount("coder"); got != 1 {
		t.Errorf("spawn count = %d after reuse, want 1", got)
	}
	if got := c.registry.getByRole("coder"); got != inst1 {
		t.Error("registry no longer points at the reused instance")
	}
}

func TestEnsureSpecialistUnknownRole(t *testing.T) {
	c := newTestCore(t)
	_, err := c.ensureSpecialist(context.Background(), "nope")
	if status.Code(err) != codes.NotFound {
		t.Errorf("err = %v, want NotFound", err)
	}
}

func TestEnsureSpecialistRejectsCoordinator(t *testing.T) {
	c := newTestCore(t)
	_, err := c.ensureSpecialist(context.Background(), "lead")
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("err = %v, want FailedPrecondition", err)
	}
}

func TestEnsureSpecialistRespawnsUnhealthy(t *testing.T) {
	c := newTestCore(t)
	fb := c.backend.(*fakeBackend)

	inst1, err := c.ensureSpecialist(context.Background(), "coder")
	if err != nil {
		t.Fatalf("first ensureSpecialist: %v", err)
	}
	oldPane := inst1.pane().PaneID()
	inst1.markExited() // agent exited: instance is now unhealthy

	inst2, err := c.ensureSpecialist(context.Background(), "coder")
	if err != nil {
		t.Fatalf("respawn ensureSpecialist: %v", err)
	}
	if inst2 == inst1 {
		t.Fatal("unhealthy instance was reused instead of respawned")
	}
	if got := fb.spawnCount("coder"); got != 2 {
		t.Errorf("spawn count = %d, want 2", got)
	}
	if got := c.registry.getByRole("coder"); got != inst2 {
		t.Error("registry does not point at the respawned instance")
	}
	if got := c.registry.get(inst1.id); got != nil {
		t.Error("stale instance is still in the registry")
	}

	var killedOld bool
	for _, id := range fb.killedIDs() {
		if id == oldPane {
			killedOld = true
		}
	}
	if !killedOld {
		t.Errorf("old pane %q was not killed on respawn (killed=%v)", oldPane, fb.killedIDs())
	}
}

// Concurrent asks for the same role must collapse into a single spawn.
func TestEnsureSpecialistConcurrentSameRoleSpawnsOnce(t *testing.T) {
	c := newTestCore(t)
	fb := c.backend.(*fakeBackend)

	const n = 20
	results := make([]*instance, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.ensureSpecialist(context.Background(), "coder")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i, inst := range results {
		if inst != results[0] {
			t.Errorf("goroutine %d got a different instance", i)
		}
	}
	if got := fb.spawnCount("coder"); got != 1 {
		t.Errorf("spawn count = %d, want 1", got)
	}
}

// Different roles hold independent spawn locks and must not serialize.
func TestEnsureSpecialistConcurrentRolesSpawnIndependently(t *testing.T) {
	c := newTestCore(t)
	fb := c.backend.(*fakeBackend)

	roles := []string{"coder", "tester", "reviewer"}
	insts := make([]*instance, len(roles))
	var wg sync.WaitGroup
	for i, r := range roles {
		wg.Add(1)
		go func(i int, r string) {
			defer wg.Done()
			inst, err := c.ensureSpecialist(context.Background(), r)
			if err != nil {
				t.Errorf("role %q: %v", r, err)
				return
			}
			insts[i] = inst
		}(i, r)
	}
	wg.Wait()

	for i, r := range roles {
		if got := fb.spawnCount(r); got != 1 {
			t.Errorf("role %q spawn count = %d, want 1", r, got)
		}
		if insts[i] == nil {
			t.Errorf("role %q produced no instance", r)
		}
	}
}
