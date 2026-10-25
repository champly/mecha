package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/creack/pty"
	"google.golang.org/grpc"
)

// testAgent launches script as the agent process on a PTY and wires it into an
// Agentd the same way startAgent does, including waitAgent as the sole
// cmd.Wait owner. It returns the descendant PID file written by the script.
func testAgent(t *testing.T, script string) (*Agentd, *exec.Cmd, string) {
	t.Helper()

	childFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "CHILD_FILE="+childFile)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start agent pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })

	a := New(Options{ID: "test-agent"})
	a.mu.Lock()
	a.cmd = cmd
	a.ptmx = ptmx
	a.agentPID = cmd.Process.Pid
	a.mu.Unlock()

	// startAgent runs waitAgent in a goroutine; mirror that contract.
	go a.waitAgent(cmd, func() {})

	return a, cmd, childFile
}

// waitPIDFile waits until the child shell has written its descendant PID.
// The marker write happens before any assertion, so there is no sleep race.
func waitPIDFile(t *testing.T, path string) int {
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
	t.Fatalf("timed out waiting for descendant PID in %s", path)
	return 0
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d is still alive", pid)
}

func waitAgentDone(t *testing.T, a *Agentd) {
	t.Helper()
	select {
	case <-a.agentDone:
	case <-time.After(5 * time.Second):
		t.Fatal("waitAgent did not reap the leader")
	}
}

func TestNewUsesLogger(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if got := New(Options{Logger: logger}).logger; got != logger {
		t.Error("New did not use the injected logger")
	}
	if New(Options{}).logger == nil {
		t.Error("default logger is nil")
	}
}

// The default (run mode) logger appends to the workspace log file.
func TestInitLoggingDefaultsToWorkspaceFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Use the resolved path so LogPath sees the same workspace string.
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}

	a := New(Options{ID: "file-log"})
	if !a.initLogging() {
		t.Fatal("initLogging failed")
	}
	if a.logFile == nil {
		t.Fatal("default initLogging did not open a log file")
	}
	t.Cleanup(func() { _ = a.logFile.Close() })

	const marker = "file-log-marker"
	a.logger.Info(marker)

	path, err := config.LogPath(workspace)
	if err != nil {
		t.Fatalf("LogPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file %q: %v", path, err)
	}
	if !strings.Contains(string(data), marker) {
		t.Errorf("log file %q missing %q; got: %s", path, marker, data)
	}
}

// LogStdout (serve mode) must not create a log file.
func TestInitLoggingStdoutSkipsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := New(Options{ID: "stdout-log", LogStdout: true})
	if !a.initLogging() {
		t.Fatal("initLogging failed")
	}
	if a.logFile != nil {
		t.Error("LogStdout created a log file")
	}
	if _, err := os.Stat(filepath.Join(home, ".mecha", "logs")); !os.IsNotExist(err) {
		t.Errorf("LogStdout created the log directory: %v", err)
	}
}

// An injected logger takes priority over the file-logging default.
func TestInitLoggingPrefersInjectedLogger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	injected := slog.New(slog.DiscardHandler)
	a := New(Options{ID: "injected-log", Logger: injected})
	if !a.initLogging() {
		t.Fatal("initLogging failed")
	}
	if a.logFile != nil {
		t.Error("injected logger created a log file")
	}
	if a.logger != injected {
		t.Error("initLogging did not use the injected logger")
	}
	if _, err := os.Stat(filepath.Join(home, ".mecha", "logs")); !os.IsNotExist(err) {
		t.Errorf("injected logger created the log directory: %v", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	a := New(Options{ID: "idle"})

	done := make(chan struct{})
	go func() {
		a.Wait()
		close(done)
	}()

	a.Stop()
	a.Stop()
	a.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Stop")
	}
}

func TestCloseUnblocksWait(t *testing.T) {
	a := New(Options{ID: "idle"})
	done := make(chan struct{})
	go func() {
		a.Wait()
		close(done)
	}()

	a.Close()
	a.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
}

// When the leader ignores SIGTERM, Stop must wait out the grace period and then
// SIGKILL the whole process group, including a descendant that also ignores
// SIGTERM/HUP.
func TestStopTerminatesAgentProcessGroup(t *testing.T) {
	oldGrace, oldKill := agentStopGrace, agentKillWait
	agentStopGrace = 150 * time.Millisecond
	agentKillWait = 3 * time.Second
	defer func() { agentStopGrace, agentKillWait = oldGrace, oldKill }()

	a, cmd, childFile := testAgent(t, `
trap '' TERM HUP
sh -c 'trap "" TERM HUP; exec sleep 300' &
echo $! > "$CHILD_FILE"
wait
`)
	leader := cmd.Process.Pid
	child := waitPIDFile(t, childFile)

	start := time.Now()
	a.Stop()
	elapsed := time.Since(start)

	waitAgentDone(t, a)
	if elapsed < agentStopGrace {
		t.Errorf("Stop returned after %v, before the %v grace period", elapsed, agentStopGrace)
	}
	if elapsed > 10*time.Second {
		t.Errorf("Stop took %v, want bounded shutdown", elapsed)
	}
	if agentGroupExists(leader) {
		t.Errorf("process group %d still exists after Stop", leader)
	}
	waitPIDGone(t, leader)
	waitPIDGone(t, child)
}

// A leader may exit while descendants keep the PTY slave open. Stop/Close must
// still find and kill the surviving group; waitAgent must not block on the
// descendant.
func TestStopReapsDescendantsAfterLeaderExit(t *testing.T) {
	oldGrace, oldKill := agentStopGrace, agentKillWait
	agentStopGrace = 150 * time.Millisecond
	agentKillWait = 3 * time.Second
	defer func() { agentStopGrace, agentKillWait = oldGrace, oldKill }()

	a, cmd, childFile := testAgent(t, `
trap '' TERM HUP
sh -c 'trap "" TERM HUP; exec sleep 300' &
echo $! > "$CHILD_FILE"
exit 0
`)
	leader := cmd.Process.Pid
	child := waitPIDFile(t, childFile)

	// The leader exits on its own; waitAgent reaps it and clears a.cmd.
	waitAgentDone(t, a)
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		gotCmd := a.cmd
		a.mu.Unlock()
		if gotCmd == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waitAgent did not clear a.cmd after the leader exited")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !pidAlive(child) {
		t.Fatal("descendant exited early; test cannot verify orphan reaping")
	}
	if !agentGroupExists(leader) {
		t.Fatal("descendant process group is missing before Stop")
	}

	start := time.Now()
	a.Stop()
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Errorf("Stop took %v, want bounded shutdown", elapsed)
	}
	if agentGroupExists(leader) {
		t.Errorf("process group %d still exists after Stop", leader)
	}
	waitPIDGone(t, child)
}

// fakeTaskStream satisfies the TaskChannel client interface without a real
// gRPC stream, so tests can drive handleTask directly.
type fakeTaskStream struct {
	grpc.ClientStream
	sent chan *api.TaskResult
}

func (s *fakeTaskStream) Send(r *api.TaskResult) error {
	select {
	case s.sent <- r:
	default:
	}
	return nil
}

func (s *fakeTaskStream) Recv() (*api.TaskRequest, error) { return nil, io.EOF }

// TestHandleTaskDoesNotHoldLockDuringPTYWrite proves handleTask releases a.mu
// before an I/O that may block. stopAgent needs a.mu to read the agent PID and
// close the PTY, so holding it across a blocked write would wedge shutdown and
// let Core SIGKILL agentd before the independent-session agent is reaped.
func TestHandleTaskDoesNotHoldLockDuringPTYWrite(t *testing.T) {
	a := New(Options{ID: "task-write-lock", Logger: slog.New(slog.DiscardHandler)})
	close(a.ready)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})
	a.mu.Lock()
	a.ptmx = pw
	a.mu.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	a.ptyWrite = func(*os.File, string) (int, error) {
		once.Do(func() { close(entered) })
		<-release
		return 0, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.handleTask(&fakeTaskStream{sent: make(chan *api.TaskResult, 2)}, &api.TaskRequest{
			Id:   "lock-check",
			Task: "hello",
		})
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handleTask did not start the PTY write")
	}

	// The writer is blocked in ptyWrite now; a.mu must still be available.
	locked := make(chan struct{})
	go func() {
		a.mu.Lock()
		close(locked)
		a.mu.Unlock()
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("handleTask held a.mu across a blocking PTY write")
	}

	stopDone := make(chan struct{})
	go func() {
		a.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked behind an in-flight PTY write")
	}

	unblock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleTask did not return after Stop")
	}
}

// TestStopReapsAgentWhileTaskWriteBlocks reproduces the real failure mode: the
// agent never reads its PTY, so a large task write fills the kernel buffer and
// blocks. Stop must still terminate the agent's process group, which is only
// possible if handleTask does not hold a.mu while writing.
func TestStopReapsAgentWhileTaskWriteBlocks(t *testing.T) {
	oldGrace, oldKill := agentStopGrace, agentKillWait
	agentStopGrace = 150 * time.Millisecond
	agentKillWait = 500 * time.Millisecond
	defer func() { agentStopGrace, agentKillWait = oldGrace, oldKill }()

	a, cmd, _ := testAgent(t, `exec sleep 300`)
	leader := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	close(a.ready)

	writeStarted := make(chan struct{})
	var once sync.Once
	a.ptyWrite = func(f *os.File, s string) (int, error) {
		once.Do(func() { close(writeStarted) })
		return io.WriteString(f, s)
	}

	go a.handleTask(&fakeTaskStream{sent: make(chan *api.TaskResult, 2)}, &api.TaskRequest{
		Id:   "blocked-write",
		Task: strings.Repeat("x", 4<<20),
	})

	select {
	case <-writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("handleTask did not start the PTY write")
	}

	// sleep does not consume stdin, so the 4 MiB payload cannot drain.
	start := time.Now()
	stopDone := make(chan struct{})
	go func() {
		a.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked behind a PTY write while the agent still owns the process group")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Stop took %v, want bounded shutdown", elapsed)
	}

	waitAgentDone(t, a)
	if agentGroupExists(leader) {
		t.Errorf("process group %d still exists after Stop", leader)
	}
	waitPIDGone(t, leader)
}

// TestStopReapsIndependentSessionDescendant reproduces a tool that starts its
// command with setsid(2). The descendant shares no process group with the
// agent leader, so only the per-PID descendant scan can reap it.
func TestStopReapsIndependentSessionDescendant(t *testing.T) {
	oldGrace, oldKill := agentStopGrace, agentKillWait
	// The descendant scan forks ps(1); leave enough room for it under -race
	// and slow CI while keeping the overall Stop assertion at 10s.
	agentStopGrace = time.Second
	agentKillWait = time.Second
	defer func() { agentStopGrace, agentKillWait = oldGrace, oldKill }()

	script := fmt.Sprintf(`AGENTD_SETSID_HELPER=1 exec %q -test.run=^TestAgentdSetsidHelper$`, os.Args[0])
	a, cmd, childFile := testAgent(t, script)
	leader := cmd.Process.Pid
	child := waitPIDFile(t, childFile)
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = syscall.Kill(-leader, syscall.SIGKILL)
	})

	pgid, err := syscall.Getpgid(child)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	if pgid == leader {
		t.Fatalf("child shares agent process group %d; test cannot detect escaped descendant", pgid)
	}
	if pgid != child {
		t.Fatalf("child pgid = %d, want its own session/group %d", pgid, child)
	}
	if !pidAlive(child) {
		t.Fatal("independent-session descendant exited before Stop")
	}

	start := time.Now()
	a.Stop()
	elapsed := time.Since(start)

	waitAgentDone(t, a)
	if elapsed > 10*time.Second {
		t.Errorf("Stop took %v, want bounded shutdown", elapsed)
	}
	if agentGroupExists(leader) {
		t.Errorf("process group %d still exists after Stop", leader)
	}
	waitPIDGone(t, leader)
	waitPIDGone(t, child)
}

// TestCloseStopsAgentBeforeSlowWebhook proves Close does not let a wedged
// webhook handler delay cleanup of the independent-session agent. The agent
// ignores TERM/HUP, so only the SIGKILL escalation can remove its group.
func TestCloseStopsAgentBeforeSlowWebhook(t *testing.T) {
	oldGrace, oldKill := agentStopGrace, agentKillWait
	agentStopGrace = 150 * time.Millisecond
	agentKillWait = 500 * time.Millisecond
	defer func() { agentStopGrace, agentKillWait = oldGrace, oldKill }()

	a, cmd, childFile := testAgent(t, `
trap '' TERM HUP
sh -c 'trap "" TERM HUP; exec sleep 300' &
echo $! > "$CHILD_FILE"
wait
`)
	leader := cmd.Process.Pid
	child := waitPIDFile(t, childFile)

	srv, err := NewWebhookServer(a.hookCh)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		_ = srv.Close()
	}()
	srv.SetParseFunc(func([]byte) (agentprovider.HookEvent, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return agentprovider.HookEvent{Event: agentprovider.EventStop}, nil
	})
	a.webhook = srv

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json", strings.NewReader("{}"))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook handler did not start")
	}

	start := time.Now()
	closeDone := make(chan struct{})
	go func() {
		a.Close()
		close(closeDone)
	}()

	// Agent cleanup must complete while the webhook handler is still blocked.
	deadline := time.Now().Add(2 * time.Second)
	for agentGroupExists(leader) {
		if time.Now().After(deadline) {
			t.Fatal("agent process group was not reaped before webhook close completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned before the blocked webhook was released")
	default:
	}
	if pidAlive(child) {
		t.Fatalf("agent descendant %d is still alive after agent cleanup", child)
	}

	close(release)
	released = true
	select {
	case <-closeDone:
	case <-time.After(agentdCloseBudget + time.Second):
		t.Fatal("Close did not finish within agentd budget")
	}
	<-clientDone
	elapsed := time.Since(start)
	if elapsed > agentdCloseBudget {
		t.Errorf("Close took %v, want <= %v", elapsed, agentdCloseBudget)
	}
	if agentGroupExists(leader) {
		t.Errorf("process group %d still exists after Close", leader)
	}
	waitPIDGone(t, child)
	t.Logf("agent reaped before slow webhook returned; Close took %v", elapsed)
}

func TestAgentStopBudgets(t *testing.T) {
	if defaultAgentStopGrace != 2*time.Second {
		t.Errorf("defaultAgentStopGrace = %v, want 2s", defaultAgentStopGrace)
	}
	if defaultAgentKillWait != time.Second {
		t.Errorf("defaultAgentKillWait = %v, want 1s", defaultAgentKillWait)
	}
	if webhookCloseTimeout != time.Second {
		t.Errorf("webhookCloseTimeout = %v, want 1s", webhookCloseTimeout)
	}
	if agentdCloseBudget != 4*time.Second {
		t.Errorf("agentdCloseBudget = %v, want 4s", agentdCloseBudget)
	}
}

// blockingRegisterCore accepts connections but never answers Register, so the
// test can prove that a signal-aware startup context aborts Start promptly.
type blockingRegisterCore struct {
	api.UnimplementedCoreServer
	registered chan struct{}
}

func (c *blockingRegisterCore) Register(ctx context.Context, _ *api.RegisterRequest) (*api.RegisterResponse, error) {
	close(c.registered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestStartAbortsWhenContextCanceled(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	coreSrv := &blockingRegisterCore{registered: make(chan struct{})}
	srv := grpc.NewServer()
	api.RegisterCoreServer(srv, coreSrv)
	go srv.Serve(lis)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	a := New(Options{
		ID:       "startup-abort",
		CoreAddr: lis.Addr().String(),
		Context:  ctx,
		Logger:   slog.New(slog.DiscardHandler),
	})
	errCh := make(chan error, 1)
	go func() { errCh <- a.Start() }()

	select {
	case <-coreSrv.registered:
	case err := <-errCh:
		t.Fatalf("Start failed before Register: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("agentd did not reach Register")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Start returned nil after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not abort after context cancellation")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Start took %v to abort, want < 2s", elapsed)
	}
}
