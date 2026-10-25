package headless

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/term/driver"
)

// syncBuffer is safe to read while the backend's PTY copy goroutine writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func spawn(t *testing.T, b *Backend, spec driver.Spec) *Handle {
	t.Helper()
	raw, err := b.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	h, ok := raw.(*Handle)
	if !ok {
		t.Fatalf("Spawn returned %T, want *Handle", raw)
	}
	t.Cleanup(func() { _ = b.Kill(context.Background(), h) })
	return h
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func waitOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	waitFor(t, 5*time.Second, "output "+strconv.Quote(want), func() bool {
		return strings.Contains(out.String(), want)
	})
}

// lastChildReadyPID parses a complete "CHILD_READY:<pid>" line. Requiring the
// trailing newline avoids racing a partially written PID through the PTY.
func lastChildReadyPID(t *testing.T, out *syncBuffer) int {
	t.Helper()
	const marker = "CHILD_READY:"
	s := out.String()
	i := strings.LastIndex(s, marker)
	if i < 0 {
		t.Fatalf("no %s in output %q", marker, s)
	}
	rest := s[i+len(marker):]
	j := strings.IndexAny(rest, "\r\n")
	if j < 0 {
		t.Fatalf("incomplete %s token in output %q", marker, s)
	}
	pid, err := strconv.Atoi(rest[:j])
	if err != nil {
		t.Fatalf("parse child pid from %q: %v", rest[:j], err)
	}
	return pid
}

func TestSpawnOutputAndInput(t *testing.T) {
	b := New(Config{GracePeriod: 2 * time.Second})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Role:    "echo",
		Command: []string{"sh", "-c", `printf 'READY\n'; IFS= read -r line; printf 'GOT:%s\n' "$line"`},
		Stdout:  &out,
	})

	if got, want := h.PaneID(), strconv.Itoa(h.PID()); got != want {
		t.Errorf("PaneID = %q, want %q", got, want)
	}
	if got, want := h.ID(), "echo-"+h.PaneID(); got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}

	waitOutput(t, &out, "READY")
	if _, err := h.Write([]byte("hello world\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitOutput(t, &out, "GOT:hello world")

	if err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v (output %q)", err, out.String())
	}
}

func TestGracefulStop(t *testing.T) {
	b := New(Config{GracePeriod: 5 * time.Second})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `trap 'printf "BYE\n"; exit 0' TERM; printf 'READY\n'; while :; do sleep 0.05; done`},
		Stdout:  &out,
	})
	waitOutput(t, &out, "READY")

	start := time.Now()
	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Errorf("graceful stop took %v; expected it to exit well before the 5s grace", elapsed)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait after graceful stop: %v", err)
	}
	if !strings.Contains(out.String(), "BYE") {
		t.Errorf("output %q missing BYE", out.String())
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	const grace = 100 * time.Millisecond
	b := New(Config{GracePeriod: grace})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `trap '' HUP TERM; printf 'READY\n'; while :; do sleep 1; done`},
		Stdout:  &out,
	})
	waitOutput(t, &out, "READY")

	start := time.Now()
	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < grace/2 {
		t.Errorf("stop returned after %v; expected to wait for the %v grace period", elapsed, grace)
	}
	if elapsed > 5*time.Second {
		t.Errorf("stop took %v; SIGKILL escalation did not work", elapsed)
	}
	if err := h.Wait(); err == nil {
		t.Error("Wait returned nil, want a signal error after SIGKILL")
	}
}

// TestProcessGroupChildrenReaped verifies that Stop waits for the whole
// process group, not just the leader. The child announces itself only after it
// has installed its signal handlers, eliminating the trap-install race.
func TestProcessGroupChildrenReaped(t *testing.T) {
	const grace = 100 * time.Millisecond
	b := New(Config{GracePeriod: grace})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `sh -c 'trap "" HUP TERM; printf "CHILD_READY:%s\n" "$$"; exec sleep 300' & printf 'LEADER_READY\n'; wait`},
		Stdout:  &out,
	})
	waitOutput(t, &out, "LEADER_READY")
	waitOutput(t, &out, "CHILD_READY:")
	childPID := lastChildReadyPID(t, &out)

	start := time.Now()
	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed < grace/2 {
		t.Errorf("stop returned after %v; the SIGTERM-ignoring child should force the full grace period", elapsed)
	}

	waitFor(t, 5*time.Second, "grandchild to be gone", func() bool {
		err := syscall.Kill(childPID, 0)
		return errors.Is(err, syscall.ESRCH)
	})
}

func TestRepeatedStopAndWait(t *testing.T) {
	b := New(Config{GracePeriod: 2 * time.Second})

	h := spawn(t, b, driver.Spec{Command: []string{"sleep", "30"}})

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- b.Kill(context.Background(), h)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Kill: %v", err)
		}
	}

	if err := b.Kill(context.Background(), h); err != nil {
		t.Errorf("repeated Kill: %v", err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Errorf("repeated Stop: %v", err)
	}
	if err := h.Wait(); err == nil {
		t.Error("Wait returned nil, want a signal error")
	}
	if _, err := h.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write after stop = %v, want io.ErrClosedPipe", err)
	}

	if err := b.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("repeated Close: %v", err)
	}
	if _, err := b.Spawn(context.Background(), driver.Spec{Command: []string{"true"}}); !errors.Is(err, ErrClosed) {
		t.Errorf("Spawn after Close = %v, want ErrClosed", err)
	}
}

func TestEnvDefaultsAndOverrides(t *testing.T) {
	b := New(Config{
		BaseEnv:     []string{"PATH=/base/bin", "HOME=/base/home", "TERM=dumb"},
		GracePeriod: 2 * time.Second,
	})
	defer b.Close()

	script := []string{"sh", "-c", `printf 'TERM=%s\nPATH=%s\nHOME=%s\nX=%s\n' "$TERM" "$PATH" "$HOME" "$MECHA_TEST"`}

	var first syncBuffer
	h1 := spawn(t, b, driver.Spec{Command: script, Stdout: &first})
	if err := h1.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for _, want := range []string{"TERM=xterm-256color", "PATH=/base/bin", "HOME=/base/home"} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("default env output %q missing %q", first.String(), want)
		}
	}

	var second syncBuffer
	h2 := spawn(t, b, driver.Spec{
		Command: script,
		Env:     []string{"TERM=screen-256color", "MECHA_TEST=1"},
		Stdout:  &second,
	})
	if err := h2.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for _, want := range []string{"TERM=screen-256color", "PATH=/base/bin", "HOME=/base/home", "X=1"} {
		if !strings.Contains(second.String(), want) {
			t.Errorf("override env output %q missing %q", second.String(), want)
		}
	}
}

func TestPTYSize(t *testing.T) {
	b := New(Config{
		Rows:        40,
		Cols:        100,
		GracePeriod: 2 * time.Second,
	})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{Command: []string{"sh", "-c", "stty size"}, Stdout: &out})
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v (output %q)", err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "40 100" {
		t.Errorf("stty size = %q, want %q", got, "40 100")
	}
}

func TestSpawnContextCancellationStopsProcess(t *testing.T) {
	const grace = 100 * time.Millisecond
	b := New(Config{GracePeriod: grace})
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	raw, err := b.Spawn(ctx, driver.Spec{Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	h := raw.(*Handle)

	cancel()
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not stop after context cancellation")
	}
	if err := h.Wait(); err == nil {
		t.Error("Wait returned nil, want a signal error")
	}
}

func TestMultipleSpawns(t *testing.T) {
	b := New(Config{GracePeriod: 2 * time.Second})
	defer b.Close()

	var out1, out2 syncBuffer
	h1 := spawn(t, b, driver.Spec{Command: []string{"sh", "-c", "echo one"}, Stdout: &out1})
	h2 := spawn(t, b, driver.Spec{Command: []string{"sh", "-c", "echo two"}, Stdout: &out2})
	if err := h1.Wait(); err != nil {
		t.Errorf("first Wait: %v", err)
	}
	if err := h2.Wait(); err != nil {
		t.Errorf("second Wait: %v", err)
	}
	if !strings.Contains(out1.String(), "one") || strings.Contains(out1.String(), "two") {
		t.Errorf("first output = %q", out1.String())
	}
	if !strings.Contains(out2.String(), "two") || strings.Contains(out2.String(), "one") {
		t.Errorf("second output = %q", out2.String())
	}
}

func TestStderrSink(t *testing.T) {
	b := New(Config{GracePeriod: 2 * time.Second})
	defer b.Close()

	var out, errOut syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `printf 'OUT\n'; printf 'ERR\n' >&2`},
		Stdout:  &out,
		Stderr:  &errOut,
	})
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(out.String(), "OUT") || strings.Contains(out.String(), "ERR") {
		t.Errorf("stdout = %q, want only OUT", out.String())
	}
	if !strings.Contains(errOut.String(), "ERR") {
		t.Errorf("stderr = %q, want ERR", errOut.String())
	}
}

// TestConfigWriterSerialized exercises Config.Stdout with a deliberately
// non-thread-safe writer shared by many spawns. Run with -race to verify the
// backend serializes those writes.
func TestConfigWriterSerialized(t *testing.T) {
	var buf bytes.Buffer // intentionally not synchronized
	b := New(Config{Stdout: &buf, GracePeriod: 2 * time.Second})
	defer b.Close()

	const spawns = 8
	handles := make([]*Handle, 0, spawns)
	for range spawns {
		h := spawn(t, b, driver.Spec{
			Command: []string{"sh", "-c", `i=0; while [ "$i" -lt 200 ]; do printf X; i=$((i+1)); done`},
		})
		handles = append(handles, h)
	}
	for _, h := range handles {
		if err := h.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	if got := strings.Count(buf.String(), "X"); got != spawns*200 {
		t.Errorf("captured %d X bytes, want %d", got, spawns*200)
	}
}

// blockWriter blocks its first Write until release is called, simulating a
// wedged log sink while the PTY reader has data.
type blockWriter struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func newBlockWriter() *blockWriter {
	return &blockWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func (w *blockWriter) Release() { close(w.release) }

// TestWaitBoundedWhenOutputWriterBlocks checks the "output truncated" path: a
// wedged writer must not make Done/Wait block forever, and the truncation must
// be observable via ErrOutputTruncated.
func TestWaitBoundedWhenOutputWriterBlocks(t *testing.T) {
	b := New(Config{GracePeriod: time.Second, DrainTimeout: 50 * time.Millisecond})
	defer b.Close()

	w := newBlockWriter()
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `printf 'READY\n'; exit 0`},
		Stdout:  w,
	})
	select {
	case <-w.started:
	case <-time.After(2 * time.Second):
		t.Fatal("writer was never called")
	}
	// Let the drain timeout expire, then unblock the writer so the reader can
	// observe the stop signal.
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.Release()
	}()

	start := time.Now()
	err := h.Wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Wait took %v with a wedged writer; want bounded", elapsed)
	}
	if !errors.Is(err, ErrOutputTruncated) {
		t.Errorf("Wait error = %v, want ErrOutputTruncated", err)
	}
}

// groupMembers returns process-table lines for every process whose PGID is
// pgid, using ps so the check is independent of the backend's state.
func groupMembers(t *testing.T, pgid int) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,stat=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var members []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		g, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if g == pgid {
			members = append(members, strings.TrimSpace(line))
		}
	}
	return members
}

func pgrepGroup(t *testing.T, pgid int) []string {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		return nil
	}
	out, err := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("pgrep -g %d: %v", pgid, err)
		}
		return nil
	}
	return strings.Fields(string(out))
}

// ptmxFDCount returns the number of PTY master descriptors held by this test
// process. lsof is used on platforms without /proc; when neither is available
// the caller skips the descriptor half of the check.
func ptmxFDCount(t *testing.T) (int, bool) {
	t.Helper()
	if lsof, err := exec.LookPath("lsof"); err == nil {
		out, err := exec.Command(lsof, "-nP", "-p", strconv.Itoa(os.Getpid()), "-F", "pcfn").Output()
		if err != nil {
			t.Fatalf("lsof: %v", err)
		}
		n := 0
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasSuffix(strings.TrimPrefix(line, "n"), "/ptmx") {
				n++
			}
		}
		return n, true
	}
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("read /proc/self/fd: %v", err)
		}
		n := 0
		for _, entry := range entries {
			target, err := os.Readlink("/proc/self/fd/" + entry.Name())
			if err == nil && strings.HasSuffix(target, "/ptmx") {
				n++
			}
		}
		return n, true
	}
	return 0, false
}

func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	waitFor(t, 5*time.Second, fmt.Sprintf("process group %d to disappear", pgid), func() bool {
		return len(groupMembers(t, pgid)) == 0
	})
}

// TestControllingTerminal checks the controlling terminal: the child is a session leader with
// the PTY as its controlling terminal, so /dev/tty is usable and TPGID == PID.
func TestControllingTerminal(t *testing.T) {
	b := New(Config{GracePeriod: time.Second})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `printf 'PID=%s TPGID=%s STAT=%s\n' "$$" "$(ps -o tpgid= -p $$ | tr -d ' ')" "$(ps -o stat= -p $$ | tr -d ' ')"; if ( : < /dev/tty ) 2>/dev/null; then printf 'TTY=ok\n'; else printf 'TTY=no\n'; fi`},
		Stdout:  &out,
	})
	if err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v (output %q)", err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "TTY=ok") {
		t.Errorf("child could not open /dev/tty: %q", s)
	}
	var pid, tpgid int
	var stat string
	if _, err := fmt.Sscanf(firstLineWithPrefix(s, "PID="), "PID=%d TPGID=%d STAT=%s", &pid, &tpgid, &stat); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	if pid != h.PID() {
		t.Errorf("shell PID = %d, handle PID = %d", pid, h.PID())
	}
	if tpgid != pid {
		t.Errorf("TPGID = %d, want PID %d (controlling terminal foreground group)", tpgid, pid)
	}
	if !strings.Contains(stat, "s") {
		t.Errorf("ps STAT = %q, want session-leader flag s", stat)
	}
	t.Logf("controlling terminal: PID=%d TPGID=%d STAT=%s /dev/tty open", pid, tpgid, stat)
}

func firstLineWithPrefix(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// spawnExitedLeaderWithDescendant starts a leader that exits immediately after
// starting a descendant which ignores SIGHUP/SIGTERM and holds the process
// group. The leader waits for a ready file so the child has installed its
// handlers before the session leader exits (avoiding the trap-install race).
func spawnExitedLeaderWithDescendant(t *testing.T, b *Backend, redirectStdio bool) (*Handle, int) {
	t.Helper()
	ready := t.TempDir() + "/ready"
	child := `sh -c 'trap "" HUP TERM; : > "$MECHA_READY"; exec sleep 300'`
	if redirectStdio {
		// The descendant keeps its process group but not the PTY slave, so the
		// leader's Done closes while the group is still alive.
		child = `sh -c 'trap "" HUP TERM; : > "$MECHA_READY"; exec sleep 300 </dev/null >/dev/null 2>&1'`
	}
	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", child + ` & child=$!; while [ ! -e "$MECHA_READY" ]; do sleep 0.01; done; printf 'CHILD_READY:%s\n' "$child"; printf 'LEADER_READY\n'; exit 0`},
		Env:     []string{"MECHA_READY=" + ready},
		Stdout:  &out,
	})
	waitOutput(t, &out, "LEADER_READY")
	waitOutput(t, &out, "CHILD_READY:")
	childPID := lastChildReadyPID(t, &out)
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not become done")
	}
	return h, childPID
}

// TestStopAfterLeaderExitRemovesGroup checks leader-exit cleanup: after the leader has
// exited, the handle stays registered and both Kill and Close still clean up a
// descendant that ignores SIGTERM.
func TestStopAfterLeaderExitRemovesGroup(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		redirect bool
	}{
		{"kill-pty", false},
		{"close-pty", false},
		{"kill-redirected", true},
		{"close-redirected", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			const grace = 100 * time.Millisecond
			b := New(Config{GracePeriod: grace})
			defer b.Close()

			h, childPID := spawnExitedLeaderWithDescendant(t, b, tc.redirect)
			before := groupMembers(t, h.PID())
			if len(before) < 1 {
				t.Fatalf("expected leader-exited group %d to still contain the descendant, got %v", h.PID(), before)
			}
			t.Logf("leader exited; surviving group %d: %v", h.PID(), before)

			start := time.Now()
			var err error
			if strings.HasPrefix(tc.mode, "kill") {
				err = b.Kill(context.Background(), h)
			} else {
				err = b.Close()
			}
			if err != nil {
				t.Fatalf("%s after leader exit: %v", tc.mode, err)
			}
			if elapsed := time.Since(start); elapsed < grace/2 {
				t.Errorf("%s returned after %v; the SIGTERM-ignoring descendant should force the grace period", tc.mode, elapsed)
			}

			waitGroupGone(t, h.PID())
			after := groupMembers(t, h.PID())
			if got := pgrepGroup(t, h.PID()); len(got) != 0 {
				t.Errorf("pgrep -g %d still reports processes: %v", h.PID(), got)
			}
			if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("descendant %d still exists after %s: %v", childPID, tc.mode, err)
			}
			t.Logf("after %s: ps=%v descendant_alive=%v", tc.mode, after, syscall.Kill(childPID, 0) == nil)
		})
	}
}

// TestStopRemovesWholeProcessGroup covers the leader-still-alive case and
// checks the OS process table after Stop.
func TestStopRemovesWholeProcessGroup(t *testing.T) {
	const grace = 100 * time.Millisecond
	b := New(Config{GracePeriod: grace})
	defer b.Close()

	var out syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `sh -c 'trap "" HUP TERM; printf "CHILD_READY:%s\n" "$$"; exec sleep 300' & printf 'LEADER_READY\n'; wait`},
		Stdout:  &out,
	})
	waitOutput(t, &out, "LEADER_READY")
	waitOutput(t, &out, "CHILD_READY:")
	childPID := lastChildReadyPID(t, &out)

	before := groupMembers(t, h.PID())
	if len(before) < 2 {
		t.Fatalf("expected leader %d and child %d in group %d, got %v", h.PID(), childPID, h.PID(), before)
	}

	start := time.Now()
	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed < grace/2 {
		t.Errorf("Stop returned after %v; expected the SIGTERM-ignoring child to force the grace period", elapsed)
	}

	waitGroupGone(t, h.PID())
	if got := pgrepGroup(t, h.PID()); len(got) != 0 {
		t.Errorf("pgrep -g %d still reports processes: %v", h.PID(), got)
	}
	if err := h.Wait(); err == nil {
		t.Error("Wait returned nil; expected a signal error for the SIGKILLed leader")
	}
}

// TestWaitBoundedWithDescendantHoldingStderr checks bounded Wait with a descendant holding stderr: a descendant
// inheriting the exec stderr pipe must not make cmd.Wait hang forever.
func TestWaitBoundedWithDescendantHoldingStderr(t *testing.T) {
	b := New(Config{GracePeriod: 100 * time.Millisecond, DrainTimeout: 60 * time.Millisecond})
	defer b.Close()

	ready := t.TempDir() + "/ready"
	var out, errOut syncBuffer
	h := spawn(t, b, driver.Spec{
		Command: []string{"sh", "-c", `sh -c 'trap "" HUP TERM; : > "$MECHA_READY"; exec sleep 300 0>/dev/null 1>/dev/null 2>&2' & while [ ! -e "$MECHA_READY" ]; do sleep 0.01; done; printf 'LEADER_READY\n'; exit 0`},
		Env:     []string{"MECHA_READY=" + ready},
		Stdout:  &out,
		Stderr:  &errOut,
	})
	waitOutput(t, &out, "LEADER_READY")

	start := time.Now()
	err := h.Wait()
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("Wait took %v with a descendant holding stderr; want bounded", elapsed)
	}
	if err == nil {
		t.Error("Wait returned nil, want an error (ErrWaitDelay/truncation)")
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Logf("Wait error = %v (expected ErrWaitDelay on this platform)", err)
	}

	t.Logf("descendant holding stderr: Wait returned in %v with err=%v", elapsed, err)
	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("cleanup Kill: %v", err)
	}
	waitGroupGone(t, h.PID())
}

// TestWaitBoundedWithDescendantHoldingPTY checks bounded Wait with a descendant holding the PTY: a descendant that
// keeps the PTY slave open and ignores SIGHUP must not make Done/Wait hang.
func TestWaitBoundedWithDescendantHoldingPTY(t *testing.T) {
	b := New(Config{GracePeriod: 100 * time.Millisecond, DrainTimeout: 60 * time.Millisecond})
	defer b.Close()

	beforeFDs, fdOK := ptmxFDCount(t)
	start := time.Now()
	h, childPID := spawnExitedLeaderWithDescendant(t, b, false)
	elapsed := time.Since(start)
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done blocked with a descendant holding the PTY")
	}
	if elapsed > 3*time.Second {
		t.Errorf("spawn->Done took %v; want bounded", elapsed)
	}
	// With a controlling terminal, the session leader's exit hangs up the
	// terminal, so the reader may see a real EOF instead of a drain timeout.
	// Either way Done/Wait must be bounded; if output was forcibly truncated we
	// must report it.
	waitErr := h.Wait()
	if waitErr != nil && !errors.Is(waitErr, ErrOutputTruncated) {
		t.Errorf("Wait error = %v, want nil or ErrOutputTruncated", waitErr)
	}
	t.Logf("descendant holding PTY: spawn->Done %v, Wait err=%v", elapsed, waitErr)

	if err := b.Kill(context.Background(), h); err != nil {
		t.Fatalf("cleanup Kill: %v", err)
	}
	waitGroupGone(t, h.PID())
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("descendant %d still exists after cleanup: %v", childPID, err)
	}

	if fdOK {
		waitFor(t, 3*time.Second, "PTY master fds to be released", func() bool {
			after, ok := ptmxFDCount(t)
			return ok && after <= beforeFDs
		})
	}
}

// TestSpawnErrorsAndInvalidHandles covers error paths and checks that a failed
// spawn does not leak a PTY master descriptor.
func TestSpawnErrorsAndInvalidHandles(t *testing.T) {
	b := New(Config{GracePeriod: time.Second})
	defer b.Close()
	ctx := context.Background()

	if _, err := b.Spawn(ctx, driver.Spec{}); err == nil {
		t.Error("Spawn with empty command returned nil error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Spawn(canceled, driver.Spec{Command: []string{"true"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("Spawn with canceled context = %v, want context.Canceled", err)
	}

	before, fdMeasured := ptmxFDCount(t)
	if _, err := b.Spawn(ctx, driver.Spec{WorkDir: "/definitely/not/a/real/dir", Command: []string{"true"}}); err == nil {
		t.Error("Spawn with a nonexistent WorkDir returned nil error")
	}
	after, afterOK := ptmxFDCount(t)
	if fdMeasured && afterOK && after > before {
		t.Errorf("failed Spawn leaked PTY master fds: %d -> %d", before, after)
	}

	if err := b.Kill(ctx, nil); err == nil {
		t.Error("Kill(nil) returned nil error")
	}
	if err := b.Kill(ctx, driver.NewHandle("foreign", "1")); err == nil {
		t.Error("Kill(foreign handle) returned nil error")
	}
}

// TestBlockedWriterRepeatedNoLeak checks the truncated-output path under
// repetition: every Wait must report ErrOutputTruncated once the writer is
// released, and neither goroutines nor PTY fds may accumulate.
func TestBlockedWriterRepeatedNoLeak(t *testing.T) {
	b := New(Config{GracePeriod: time.Second, DrainTimeout: 5 * time.Millisecond})
	defer b.Close()

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()
	beforeFDs, okFDs := ptmxFDCount(t)

	const iterations = 100
	for i := 0; i < iterations; i++ {
		w := newBlockWriter()
		raw, err := b.Spawn(context.Background(), driver.Spec{
			Command: []string{"sh", "-c", "printf 'READY\n'; exit 0"},
			Stdout:  w,
		})
		if err != nil {
			t.Fatalf("Spawn %d: %v", i, err)
		}
		h := raw.(*Handle)
		select {
		case <-w.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("writer was never called on iteration %d", i)
		}
		// The leader has exited and the drain timeout has expired; releasing
		// the writer now must not hide the truncation.
		time.Sleep(30 * time.Millisecond)
		w.Release()
		if err := h.Wait(); !errors.Is(err, ErrOutputTruncated) {
			t.Errorf("iteration %d Wait = %v, want ErrOutputTruncated", i, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= baselineGoroutines+2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterGoroutines := runtime.NumGoroutine()
	afterFDs, afterOK := ptmxFDCount(t)
	t.Logf("blocked writer x%d: goroutines %d->%d ptmx_fds %d->%d", iterations, baselineGoroutines, afterGoroutines, beforeFDs, afterFDs)
	if afterGoroutines > baselineGoroutines+2 {
		t.Errorf("goroutines grew from %d to %d", baselineGoroutines, afterGoroutines)
	}
	if okFDs && afterOK && afterFDs > beforeFDs {
		t.Errorf("PTY master fds grew from %d to %d", beforeFDs, afterFDs)
	}
}

// TestRepeatedSpawnStopNoLeak exercises repeated spawn/kill cycles and
// checks that neither goroutines nor PTY master descriptors grow.
func TestRepeatedSpawnStopNoLeak(t *testing.T) {
	b := New(Config{GracePeriod: 2 * time.Second})
	defer b.Close()

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()
	beforeFDs, okFDs := ptmxFDCount(t)
	t.Logf("baseline: goroutines=%d ptmx_fds=%d (fd measurement=%v)", baselineGoroutines, beforeFDs, okFDs)

	const iterations = 100
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		h, err := b.Spawn(ctx, driver.Spec{Command: []string{"sleep", "30"}})
		if err != nil {
			cancel()
			t.Fatalf("Spawn %d: %v", i, err)
		}
		if err := b.Kill(context.Background(), h); err != nil {
			cancel()
			t.Fatalf("Kill %d: %v", i, err)
		}
		cancel()
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= baselineGoroutines+2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterGoroutines := runtime.NumGoroutine()
	afterFDs, afterOK := ptmxFDCount(t)
	t.Logf("after %d spawn/stop: goroutines=%d ptmx_fds=%d", iterations, afterGoroutines, afterFDs)

	if afterGoroutines > baselineGoroutines+2 {
		t.Errorf("goroutines grew from %d to %d", baselineGoroutines, afterGoroutines)
	}
	if okFDs && afterOK && afterFDs > beforeFDs {
		t.Errorf("PTY master fds grew from %d to %d", beforeFDs, afterFDs)
	}
}

func TestHandleRegisteredUntilGroupGone(t *testing.T) {
	b := New(Config{GracePeriod: 100 * time.Millisecond})
	defer b.Close()

	ready := filepath.Join(t.TempDir(), "ready")
	raw, err := b.Spawn(context.Background(), driver.Spec{
		Command: []string{"sh", "-c", `sh -c 'trap "" HUP TERM; : > "$MECHA_READY"; exec sleep 300 </dev/null >/dev/null 2>&1' & while [ ! -e "$MECHA_READY" ]; do sleep 0.01; done; exit 0`},
		Env:     []string{"MECHA_READY=" + ready},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	h := raw.(*Handle)
	defer func() { _ = killGroup(h.pid) }()

	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not exit")
	}

	// The leader may have exited, but the descendant must still hold the group.
	deadline := time.Now().Add(3 * time.Second)
	for !groupExists(h.pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !groupExists(h.pid) {
		t.Fatal("descendant did not survive the leader exit")
	}

	b.mu.Lock()
	_, registered := b.handles[h]
	b.mu.Unlock()
	if !registered {
		t.Fatal("handle was removed from the backend while its process group was still alive")
	}

	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if groupExists(h.pid) {
		t.Fatalf("process group %d still exists after Stop", h.pid)
	}

	b.mu.Lock()
	_, registered = b.handles[h]
	b.mu.Unlock()
	if registered {
		t.Error("handle is still registered after its process group disappeared")
	}
}
