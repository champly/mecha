package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
	"github.com/champly/mecha/pkg/term/headless"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// waitAddr waits until Run has bound its listener and returns the address.
func waitAddr(t *testing.T, c *Core) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := c.Addr(); addr != "" {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("core did not start listening")
	return ""
}

func waitRun(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancellation")
		return nil
	}
}

func TestNewWithOptionsDefaults(t *testing.T) {
	backend := &fakeBackend{}

	c, err := NewWithOptions(Options{Workspace: t.TempDir(), Backend: backend})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if c.Logger() == nil {
		t.Error("Logger() is nil")
	}
	if got := c.Addr(); got != "" {
		t.Errorf("Addr() before Run = %q, want empty", got)
	}
	if got := c.listenAddr; got != defaultAddr {
		t.Errorf("listenAddr = %q, want %q", got, defaultAddr)
	}
	if got := c.shutdownTimeout; got != defaultShutdownTimeout {
		t.Errorf("shutdownTimeout = %v, want %v", got, defaultShutdownTimeout)
	}
}

func TestNewWithOptionsOverrides(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	c, err := NewWithOptions(Options{
		Workspace:       t.TempDir(),
		Backend:         &fakeBackend{},
		Addr:            "127.0.0.1:12345",
		Logger:          logger,
		ShutdownTimeout: 6 * time.Second,
		Serve:           true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if c.Logger() != logger {
		t.Error("Logger() did not return the injected logger")
	}
	if !c.serve {
		t.Error("serve option was not applied")
	}
	if c.listenAddr != "127.0.0.1:12345" {
		t.Errorf("listenAddr = %q", c.listenAddr)
	}
	if c.shutdownTimeout != 6*time.Second {
		t.Errorf("shutdownTimeout = %v", c.shutdownTimeout)
	}
}

func TestShutdownTimeoutFloor(t *testing.T) {
	c, err := NewWithOptions(Options{
		Workspace:       t.TempDir(),
		Backend:         &fakeBackend{},
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if c.shutdownTimeout != minPaneShutdownBudget {
		t.Errorf("shutdownTimeout = %v, want floor %v", c.shutdownTimeout, minPaneShutdownBudget)
	}
}

// Serve mode must never launch the interactive coordinator, and it must expose
// specialist Ask RPCs on the configured listener.
func TestRunServeModeServesSpecialistsOnly(t *testing.T) {
	c := newTestCore(t)
	c.serve = true
	fb := c.backend.(*fakeBackend)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	addr := waitAddr(t, c)

	// Pre-spawn the specialist so the test can answer its task. In production
	// this happens inside the gRPC Ask handler.
	inst, err := c.ensureSpecialist(context.Background(), "coder")
	if err != nil {
		t.Fatalf("ensureSpecialist: %v", err)
	}
	inst.mu.Lock()
	stream, ok := inst.stream.(*fakeTaskStream)
	inst.mu.Unlock()
	if !ok {
		t.Fatal("specialist has no fake task stream")
	}
	go func() {
		req := <-stream.sent
		inst.deliverResult(&api.AskResponse{Id: req.Id, Success: true, Result: "pong"})
	}()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial core: %v", err)
	}
	defer conn.Close()
	client := api.NewCoreClient(conn)

	askCtx, askCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer askCancel()
	resp, err := client.Ask(askCtx, &api.AskRequest{Role: "coder", Task: "ping"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.GetResult() != "pong" || !resp.GetSuccess() {
		t.Errorf("Ask = %+v, want success/pong", resp)
	}
	if got := fb.spawnCount("lead"); got != 0 {
		t.Errorf("coordinator spawn count = %d, want 0", got)
	}

	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

func TestRunServeModeKillsSpecialistPanesOnShutdown(t *testing.T) {
	c := newTestCore(t)
	c.serve = true
	fb := c.backend.(*fakeBackend)

	inst := newInstance("inst-1", "coder")
	inst.setHandle(fakeHandle{id: "h-1", pane: "pane-1"})
	c.registry.add(inst)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitAddr(t, c)

	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}

	killed := fb.killedIDs()
	if len(killed) != 1 || killed[0] != "pane-1" {
		t.Errorf("killed panes = %v, want [pane-1]", killed)
	}
}

func TestStartWithoutCoordinatorReturnsError(t *testing.T) {
	c := newTestCore(t)
	c.cfg = config.Config{
		Profile: "test",
		Profiles: map[string]config.ProfileConfig{
			"test": {Roles: []config.Role{{Name: "coder"}}},
		},
	}

	err := c.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no coordinator role") {
		t.Fatalf("Start error = %v, want no coordinator role", err)
	}
	if c.Addr() == "" {
		t.Error("listener was not started before coordinator launch")
	}
}

func TestShutdownIdempotentAndNilSafe(t *testing.T) {
	c := &Core{logger: slog.New(slog.DiscardHandler)}
	c.shutdown()
	c.shutdown()
}

// Core literals (used heavily by tests and potentially by future embedders)
// must not panic when Run starts and stops with no pane registry entries.
func TestRunServeShutdownWithEmptyRegistry(t *testing.T) {
	c := &Core{
		cfg:        config.Config{},
		logger:     slog.New(slog.DiscardHandler),
		backend:    &fakeBackend{},
		registry:   newRegistry(),
		listenAddr: defaultAddr,
		serve:      true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitAddr(t, c)
	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

func TestOnListeningReceivesActualAddr(t *testing.T) {
	c := newTestCore(t)
	c.serve = true
	addrCh := make(chan string, 1)
	c.onListening = func(addr string) { addrCh <- addr }

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	var announced string
	select {
	case announced = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("OnListening was not called")
	}
	if announced == "" || announced != c.Addr() {
		t.Errorf("OnListening addr = %q, Addr() = %q", announced, c.Addr())
	}

	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Errorf("Run returned %v, want nil", err)
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	valid := []string{
		"127.0.0.1:0",
		"127.255.0.1:8080",
		"127.0.0.1:65535",
		"[::1]:0",
		"localhost:0",
		"LOCALHOST:1234",
	}
	for _, addr := range valid {
		t.Run("valid_"+addr, func(t *testing.T) {
			if err := ValidateLoopbackAddr(addr); err != nil {
				t.Errorf("ValidateLoopbackAddr(%q) = %v, want nil", addr, err)
			}
		})
	}

	invalid := []string{
		"",
		":0",
		"0.0.0.0:0",
		"[::]:0",
		"192.0.2.1:0",
		"example.com:0",
		"127.0.0.1",
		"[::1]",
		"127.0.0.1:",
		"localhost:",
		"[::1]:",
		"127.0.0.1:abc",
		"127.0.0.1:-1",
		"127.0.0.1:65536",
		"localhost:65536",
	}
	for _, addr := range invalid {
		t.Run("invalid_"+addr, func(t *testing.T) {
			if err := ValidateLoopbackAddr(addr); err == nil {
				t.Errorf("ValidateLoopbackAddr(%q) = nil, want error", addr)
			}
		})
	}
}

func TestOnListeningPanicIsRecovered(t *testing.T) {
	var logs bytes.Buffer
	fb := &fakeBackend{}
	called := make(chan struct{})
	c, err := NewWithOptions(Options{
		Backend: fb,
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
		Serve:   true,
		OnListening: func(string) {
			close(called)
			panic("callback boom")
		},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("OnListening was not called")
	}
	waitAddr(t, c)
	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Errorf("Run returned %v after callback panic, want nil", err)
	}
	if !fb.isClosed() {
		t.Error("backend was not closed after callback panic")
	}
	if !strings.Contains(logs.String(), "onListening callback panic") {
		t.Errorf("callback panic was not logged: %q", logs.String())
	}
}

func TestRunListenerFailureClosesBackend(t *testing.T) {
	fb := &fakeBackend{}
	c, err := NewWithOptions(Options{Backend: fb, Addr: "0.0.0.0:0"})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	err = c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("Run error = %v, want non-loopback rejection", err)
	}
	if !fb.isClosed() {
		t.Error("backend was not closed after listener failure")
	}
}

func TestNormalizeServeError(t *testing.T) {
	if err := normalizeServeError(nil); err != nil {
		t.Errorf("normalizeServeError(nil) = %v", err)
	}
	if err := normalizeServeError(grpc.ErrServerStopped); err != nil {
		t.Errorf("normalizeServeError(ErrServerStopped) = %v, want nil", err)
	}
	sentinel := errors.New("serve boom")
	err := normalizeServeError(sentinel)
	if !errors.Is(err, sentinel) {
		t.Errorf("normalizeServeError(sentinel) = %v, want wrapped sentinel", err)
	}
}

func TestShutdownLogsBackendCloseErrorBeforeComplete(t *testing.T) {
	var buf bytes.Buffer
	fb := &fakeBackend{closeErr: errors.New("close boom")}
	c := &Core{
		logger:   slog.New(slog.NewTextHandler(&buf, nil)),
		backend:  fb,
		registry: newRegistry(),
	}

	c.shutdown()

	logs := buf.String()
	closeIdx := strings.Index(logs, "close backend failed")
	completeIdx := strings.Index(logs, "core shutdown complete")
	if closeIdx < 0 {
		t.Errorf("close error was not logged: %q", logs)
	}
	if completeIdx < 0 {
		t.Errorf("shutdown completion was not logged: %q", logs)
	}
	if closeIdx >= 0 && completeIdx >= 0 && closeIdx > completeIdx {
		t.Errorf("completion was logged before backend close error: %q", logs)
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out reading pid from %s", path)
	return 0
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func processGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitPIDGoneCore(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d is still alive", pid)
}

const (
	fakeAgentdHelperEnv      = "MECHA_TEST_FAKE_AGENTD_HELPER"
	fakeAgentChildHelperEnv  = "MECHA_TEST_FAKE_AGENT_CHILD_HELPER"
	fakeAgentdPIDFileEnv     = "MECHA_TEST_FAKE_AGENTD_PID_FILE"
	fakeAgentChildPIDFileEnv = "MECHA_TEST_FAKE_AGENT_CHILD_PID_FILE"
	// fakeAgentdCleanupDelay is longer than the old fixed 1s pane timeout but
	// well inside Core's 5s budget, so this test catches a Core that SIGKILLs
	// agentd before its independent-session child is reaped.
	fakeAgentdCleanupDelay = 1500 * time.Millisecond
)

// TestFakeAgentdHelper runs as a separate test binary process and mimics the
// relevant agentd tree shape: it is a session leader (headless backend does
// that), and it starts an agent child in a different session. On SIGTERM it
// waits before SIGKILLing the child group, proving Core must wait for agentd
// cleanup rather than killing agentd's group only.
func TestFakeAgentdHelper(t *testing.T) {
	if os.Getenv(fakeAgentdHelperEnv) != "1" {
		return
	}
	agentdPIDFile := os.Getenv(fakeAgentdPIDFileEnv)
	childPIDFile := os.Getenv(fakeAgentChildPIDFileEnv)

	child := exec.Command(os.Args[0], "-test.run=^TestFakeAgentChildHelper$")
	child.Env = append(os.Environ(), fakeAgentChildHelperEnv+"=1", fakeAgentChildPIDFileEnv+"="+childPIDFile)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "fake agentd: start child: %v\n", err)
		os.Exit(2)
	}
	if err := os.WriteFile(agentdPIDFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		os.Exit(2)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	<-sigCh
	time.Sleep(fakeAgentdCleanupDelay)
	_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	_ = child.Wait()
	os.Exit(0)
}

// TestFakeAgentChildHelper is the independent-session descendant. It ignores
// TERM/HUP so only agentd's explicit SIGKILL can remove it.
func TestFakeAgentChildHelper(t *testing.T) {
	if os.Getenv(fakeAgentChildHelperEnv) != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
	if err := os.WriteFile(os.Getenv(fakeAgentChildPIDFileEnv), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// TestShutdownWaitsForFakeAgentdToReapIndependentSessionChild verifies the real
// headless -> fake agentd -> independent-session child structure. Core must
// give agentd enough time to reap the child; killing only the agentd group
// would leave the child behind.
func TestShutdownWaitsForFakeAgentdToReapIndependentSessionChild(t *testing.T) {
	dir := t.TempDir()
	agentdPIDFile := filepath.Join(dir, "agentd.pid")
	childPIDFile := filepath.Join(dir, "child.pid")

	backend := headless.New(headless.Config{GracePeriod: 30 * time.Second})
	h, err := backend.Spawn(context.Background(), term.Spec{
		Command: []string{os.Args[0], "-test.run=^TestFakeAgentdHelper$"},
		Env: []string{
			fakeAgentdHelperEnv + "=1",
			fakeAgentdPIDFileEnv + "=" + agentdPIDFile,
			fakeAgentChildPIDFileEnv + "=" + childPIDFile,
		},
	})
	if err != nil {
		_ = backend.Close()
		t.Fatalf("Spawn fake agentd: %v", err)
	}
	agentdPID := readPIDFile(t, agentdPIDFile)
	childPID := readPIDFile(t, childPIDFile)
	t.Cleanup(func() {
		_ = syscall.Kill(-childPID, syscall.SIGKILL)
		_ = syscall.Kill(-agentdPID, syscall.SIGKILL)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = backend.Kill(ctx, h)
		_ = backend.Close()
	})

	pgid, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	if pgid != childPID {
		t.Fatalf("child pgid = %d, want its own session/group %d", pgid, childPID)
	}
	if pgid == agentdPID {
		t.Fatalf("child shares agentd process group %d; test cannot detect orphan", pgid)
	}

	c, err := NewWithOptions(Options{
		Workspace:       dir,
		Backend:         backend,
		Logger:          slog.New(slog.DiscardHandler),
		Serve:           true,
		ShutdownTimeout: minPaneShutdownBudget,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	inst := newInstance("inst-fake-agentd", "coder")
	inst.setHandle(h)
	c.registry.add(inst)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitAddr(t, c)

	start := time.Now()
	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > minPaneShutdownBudget+2*time.Second {
		t.Errorf("shutdown took %v, want <= %v (+2s tolerance for slow CI)", elapsed, minPaneShutdownBudget+2*time.Second)
	}
	if processGroupExists(childPID) {
		t.Errorf("independent-session child group %d survived Core shutdown", childPID)
	}
	waitPIDGoneCore(t, childPID)
	t.Logf("Core waited for fake agentd cleanup in %v (agentd=%d child=%d)", elapsed, agentdPID, childPID)
}

// TestShutdownReapsHeadlessProcessGroupWithinBudget uses the real headless
// backend with a fake agentd that ignores SIGTERM/HUP. Core's shutdown context
// must expire, escalate to SIGKILL, and leave no process behind.
func TestShutdownReapsHeadlessProcessGroupWithinBudget(t *testing.T) {
	dir := t.TempDir()
	leaderFile := filepath.Join(dir, "leader.pid")
	childFile := filepath.Join(dir, "child.pid")
	readyFile := filepath.Join(dir, "ready")

	script := `trap '' TERM HUP
echo $$ > "$LEADER_FILE"
sh -c 'trap "" TERM HUP; echo $$ > "$CHILD_FILE"; exec sleep 300' &
while [ ! -s "$CHILD_FILE" ]; do sleep 0.01; done
: > "$READY_FILE"
wait
`
	// GracePeriod is longer than the Core budget, so only Core's deadline can
	// trigger SIGKILL.
	backend := headless.New(headless.Config{GracePeriod: 30 * time.Second})
	h, err := backend.Spawn(context.Background(), term.Spec{
		Command: []string{"sh", "-c", script},
		Env: []string{
			"LEADER_FILE=" + leaderFile,
			"CHILD_FILE=" + childFile,
			"READY_FILE=" + readyFile,
		},
	})
	if err != nil {
		_ = backend.Close()
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = backend.Kill(ctx, h)
		_ = backend.Close()
	})

	leader := readPIDFile(t, leaderFile)
	child := readPIDFile(t, childFile)
	waitFile(t, readyFile)

	c, err := NewWithOptions(Options{
		Workspace:       dir,
		Backend:         backend,
		Logger:          slog.New(slog.DiscardHandler),
		Serve:           true,
		ShutdownTimeout: minPaneShutdownBudget,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	inst := newInstance("inst-headless", "coder")
	inst.setHandle(h)
	c.registry.add(inst)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitAddr(t, c)

	start := time.Now()
	cancel()
	if err := waitRun(t, errCh); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > minPaneShutdownBudget+2*time.Second {
		t.Errorf("shutdown took %v, want <= %v (+2s tolerance for slow CI)", elapsed, minPaneShutdownBudget)
	}
	if processGroupExists(leader) {
		t.Errorf("process group %d still exists after shutdown", leader)
	}
	waitPIDGoneCore(t, leader)
	waitPIDGoneCore(t, child)
	t.Logf("headless group reaped in %v (leader=%d child=%d)", elapsed, leader, child)
}
