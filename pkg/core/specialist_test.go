package core

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

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

	mu       sync.Mutex
	spawns   map[string]int
	specs    []term.Spec
	killed   []string
	seq      int
	closed   int
	closeErr error
}

func (f *fakeBackend) Spawn(_ context.Context, spec term.Spec) (term.Handle, error) {
	f.mu.Lock()
	if f.spawns == nil {
		f.spawns = make(map[string]int)
	}
	f.spawns[spec.Role]++
	f.specs = append(f.specs, spec)
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

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.closeErr
}

func (f *fakeBackend) Label(_ string) error { return nil }

func (f *fakeBackend) closeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeBackend) isClosed() bool { return f.closeCalls() > 0 }

func (f *fakeBackend) spawnCount(role string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawns[role]
}

// lastSpec returns the most recent spawn spec for role.
func (f *fakeBackend) lastSpec(role string) (term.Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.specs) - 1; i >= 0; i-- {
		if f.specs[i].Role == role {
			return f.specs[i], true
		}
	}
	return term.Spec{}, false
}

func (f *fakeBackend) killedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// blockingKillBackend keeps Kill calls in flight so tests can verify that
// shutdown starts all pane cleanup without serializing on one backend.
type blockingKillBackend struct {
	*fakeBackend
	started chan string
}

func (b *blockingKillBackend) Kill(ctx context.Context, h term.Handle) error {
	select {
	case b.started <- h.PaneID():
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
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

// serve forwards agentd logs to stdout with --log-stdout; run keeps the
// historical workspace log file.
func TestEnsureSpecialistLogStdoutOnlyInServeMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve bool
		want  bool
	}{
		{name: "run", serve: false, want: false},
		{name: "serve", serve: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCore(t)
			c.serve = tc.serve
			fb := c.backend.(*fakeBackend)

			if _, err := c.ensureSpecialist(context.Background(), "coder"); err != nil {
				t.Fatalf("ensureSpecialist: %v", err)
			}
			spec, ok := fb.lastSpec("coder")
			if !ok {
				t.Fatal("no spawn spec recorded")
			}
			if got := slices.Contains(spec.Command, "--log-stdout"); got != tc.want {
				t.Errorf("Command %v contains --log-stdout = %v, want %v", spec.Command, got, tc.want)
			}
		})
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

func TestKillAllPanesConcurrentAndBounded(t *testing.T) {
	c := newTestCore(t)
	fb := c.backend.(*fakeBackend)
	b := &blockingKillBackend{
		fakeBackend: fb,
		started:     make(chan string, 2),
	}
	c.backend = b

	for _, role := range []string{"coder", "tester"} {
		inst := newInstance("id-"+role, role)
		inst.setHandle(fakeHandle{id: "handle-" + role, pane: "pane-" + role})
		c.registry.add(inst)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.killAllPanes(ctx)
		close(done)
	}()

	started := make(map[string]bool)
	for range 2 {
		select {
		case id := <-b.started:
			started[id] = true
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatalf("pane cleanup was serialized; started = %v", started)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("killAllPanes did not return after context cancellation")
	}
}
