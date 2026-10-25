// Package headless implements a terminal backend that runs a child process on
// a private PTY without attaching it to the current terminal.
//
// The backend is intended for daemon/supervisor environments where there is no
// controlling TTY. It never reads os.Stdin, never touches the process's own
// terminal state, and always talks to the child through an explicitly sized
// PTY. Input is written through Handle.Write.
package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/champly/mecha/pkg/term/driver"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	// DefaultRows and DefaultCols are used when Config does not specify a size.
	DefaultRows uint16 = 24
	DefaultCols uint16 = 80
	// DefaultGracePeriod is how long Stop waits after SIGTERM before escalating
	// to SIGKILL.
	DefaultGracePeriod = 10 * time.Second
	// DefaultDrainTimeout bounds how long Wait keeps reading PTY output after
	// the leader exits. It is also used as exec.Cmd.WaitDelay so a descendant
	// holding a stderr pipe cannot hang Wait forever.
	DefaultDrainTimeout = 250 * time.Millisecond

	// groupPollInterval is how often the backend checks whether the whole
	// process group has exited.
	groupPollInterval = 20 * time.Millisecond
	// killWaitTimeout bounds the post-SIGKILL wait for the process group to
	// disappear.
	killWaitTimeout = 5 * time.Second
	// readerStopTimeout bounds how long we wait for the poll-driven reader to
	// notice the stop signal. It should be a few poll intervals.
	readerStopTimeout = 500 * time.Millisecond
	// baseDoneWaitTimeout is the minimum bound for Stop waiting on Wait/Done
	// bookkeeping after the process group is gone.
	baseDoneWaitTimeout = 2 * time.Second
)

var (
	// ErrClosed is returned by Spawn after Close has been called.
	ErrClosed = errors.New("headless: backend closed")
	// ErrOutputTruncated is joined into Wait's error when output could not be
	// fully drained because a descendant kept the PTY slave open.
	ErrOutputTruncated = errors.New("headless: output truncated; descendant still holds the PTY")
	// ErrWaitTimeout is returned by Stop if the leader's Wait bookkeeping does
	// not complete after its process group is gone.
	ErrWaitTimeout = errors.New("headless: timed out waiting for process exit")
)

// Config configures a headless backend. The zero value is usable: output is
// discarded, the environment is inherited, and the PTY is 80x24.
type Config struct {
	// Stdout receives the PTY output for spawns that do not set Spec.Stdout.
	// Nil means io.Discard. Config-level writers are serialized so multiple
	// spawns may share a non-thread-safe writer.
	Stdout io.Writer
	// Stderr receives the child's stderr for spawns that do not set
	// Spec.Stderr. Nil merges stderr into the PTY (and therefore into Stdout).
	// Config-level writers are serialized.
	Stderr io.Writer
	// Env is appended to (and overrides) the base environment.
	Env []string
	// BaseEnv is the base environment. Nil means os.Environ(), which preserves
	// PATH/HOME from the daemon environment.
	BaseEnv []string
	// Rows and Cols configure the PTY size. Zero selects the defaults.
	Rows uint16
	Cols uint16
	// GracePeriod is the SIGTERM-to-SIGKILL grace period. Zero selects
	// DefaultGracePeriod; a negative value escalates immediately.
	GracePeriod time.Duration
	// DrainTimeout bounds post-exit output draining. Zero selects
	// DefaultDrainTimeout; a negative value disables PTY draining (cmd.Wait is
	// still bounded by DefaultDrainTimeout for stderr pipes).
	DrainTimeout time.Duration
}

var (
	_ driver.Backend = (*Backend)(nil)
	_ io.Writer      = (*Handle)(nil)
)

// Backend is a driver.Backend that spawns children on private PTYs.
type Backend struct {
	stdout  io.Writer
	stderr  io.Writer
	env     []string
	baseEnv []string
	rows    uint16
	cols    uint16
	grace   time.Duration
	drain   time.Duration

	stdoutMu sync.Mutex
	stderrMu sync.Mutex

	mu      sync.Mutex
	closed  bool
	handles map[*Handle]struct{}
}

// New creates a headless backend. It does not allocate any resources until the
// first Spawn.
func New(cfg Config) *Backend {
	rows := cfg.Rows
	if rows == 0 {
		rows = DefaultRows
	}
	cols := cfg.Cols
	if cols == 0 {
		cols = DefaultCols
	}
	grace := cfg.GracePeriod
	switch {
	case grace == 0:
		grace = DefaultGracePeriod
	case grace < 0:
		grace = 0
	}
	drain := cfg.DrainTimeout
	switch {
	case drain == 0:
		drain = DefaultDrainTimeout
	case drain < 0:
		drain = 0
	}
	base := cfg.BaseEnv
	if base == nil {
		base = os.Environ()
	}
	b := &Backend{
		env:     append([]string(nil), cfg.Env...),
		baseEnv: append([]string(nil), base...),
		rows:    rows,
		cols:    cols,
		grace:   grace,
		drain:   drain,
		handles: make(map[*Handle]struct{}),
	}
	// Serialize writes from multiple PTY readers (or exec stderr copiers) that
	// share a Config-level writer. Spec-level writers are owned by the caller.
	if cfg.Stdout != nil && cfg.Stdout != io.Discard {
		b.stdout = &lockedWriter{mu: &b.stdoutMu, w: cfg.Stdout}
	} else {
		b.stdout = cfg.Stdout
	}
	if cfg.Stderr != nil && cfg.Stderr != io.Discard {
		b.stderr = &lockedWriter{mu: &b.stderrMu, w: cfg.Stderr}
	} else {
		b.stderr = cfg.Stderr
	}
	return b
}

// Spawn starts spec.Command on a new PTY. The child becomes a session and
// process-group leader and acquires the PTY as its controlling terminal, so
// TUI behaviour matches the interactive path. Its PGID is the child PID, which
// keeps kill(-pid, ...) valid for the whole subtree.
func (b *Backend) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	if len(spec.Command) == 0 || spec.Command[0] == "" {
		return nil, errors.New("headless: empty command")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	out := spec.Stdout
	if out == nil {
		out = b.stdout
	}
	if out == nil {
		out = io.Discard
	}
	errOut := spec.Stderr
	if errOut == nil {
		errOut = b.stderr
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("headless: open pty: %w", err)
	}
	fail := func(err error) (driver.Handle, error) {
		_ = tty.Close()
		_ = ptmx.Close()
		return nil, err
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: b.rows, Cols: b.cols}); err != nil {
		return fail(fmt.Errorf("headless: set pty size: %w", err))
	}
	// Non-blocking master + poll is required: on Darwin, closing the master is
	// not guaranteed to wake a goroutine blocked in read(2), which used to hang
	// Wait/Done forever when a descendant inherited the slave.
	if err := setNonblock(ptmx); err != nil {
		return fail(fmt.Errorf("headless: set pty nonblocking: %w", err))
	}

	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.WorkDir
	cmd.Env = b.buildEnv(spec.Env)
	// Keep Wait bounded even if the caller disabled output draining: a
	// descendant inheriting the stderr pipe must not wedge Wait forever.
	waitDelay := b.drain
	if waitDelay <= 0 {
		waitDelay = DefaultDrainTimeout
	}
	cmd.WaitDelay = waitDelay
	cmd.Stdin = tty
	cmd.Stdout = tty

	var stderrFile *os.File
	switch {
	case errOut == nil:
		// Merge stderr into the terminal, matching a normal interactive process.
		cmd.Stderr = tty
	case errOut == io.Discard:
		// Avoid an exec pipe (and its goroutine) when the caller asked to
		// discard stderr; a descendant inheriting that pipe could otherwise
		// block cmd.Wait indefinitely.
		f, ferr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if ferr != nil {
			return fail(fmt.Errorf("headless: open %s: %w", os.DevNull, ferr))
		}
		stderrFile = f
		cmd.Stderr = f
	default:
		cmd.Stderr = errOut
	}
	cmd.SysProcAttr = newSysProcAttr()

	// Hold the lock across registration and Start so Close cannot race with a
	// new child being created.
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		if stderrFile != nil {
			_ = stderrFile.Close()
		}
		return fail(ErrClosed)
	}
	err = cmd.Start()
	if stderrFile != nil {
		_ = stderrFile.Close()
	}
	_ = tty.Close()
	if err != nil {
		b.mu.Unlock()
		_ = ptmx.Close()
		return nil, fmt.Errorf("headless: start %q: %w", spec.Command[0], err)
	}
	pid := cmd.Process.Pid
	h := &Handle{
		backend:    b,
		cmd:        cmd,
		ptmx:       ptmx,
		pid:        pid,
		id:         handleID(spec.Role, pid),
		paneID:     strconv.Itoa(pid),
		done:       make(chan struct{}),
		outputDone: make(chan struct{}),
		stopRead:   make(chan struct{}),
		groupGone:  make(chan struct{}),
	}
	b.handles[h] = struct{}{}
	b.mu.Unlock()

	go h.copyOutput(out, ptmx)
	go h.wait()

	if ctx.Done() != nil {
		go h.watchContext(ctx)
	}
	return h, nil
}

// Kill terminates a handle created by this backend. It is idempotent.
func (b *Backend) Kill(ctx context.Context, handle driver.Handle) error {
	h, ok := handle.(*Handle)
	if !ok || h == nil {
		return fmt.Errorf("headless: invalid handle %T", handle)
	}
	return h.Stop(ctx)
}

// Close terminates every live child. It is idempotent. Handles whose leader
// already exited but whose process group still has descendants are included.
func (b *Backend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	handles := make([]*Handle, 0, len(b.handles))
	for h := range b.handles {
		handles = append(handles, h)
	}
	b.mu.Unlock()

	var firstErr error
	for _, h := range handles {
		if err := h.Stop(context.Background()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Label is a no-op; a headless backend has no visible pane to label.
func (b *Backend) Label(string) error { return nil }

func (b *Backend) buildEnv(extra []string) []string {
	env := make([]string, 0, len(b.baseEnv)+len(b.env)+len(extra)+3)
	index := make(map[string]int, len(b.baseEnv)+len(b.env)+len(extra)+3)

	add := func(entry string) {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return
		}
		if i, exists := index[key]; exists {
			env[i] = entry
			return
		}
		index[key] = len(env)
		env = append(env, entry)
	}

	for _, entry := range b.baseEnv {
		add(entry)
	}
	// The child has no terminal of its own beyond this PTY. Override whatever
	// the supervisor inherited (launchd/systemd often provide TERM=dumb or
	// nothing) so TUIs see a sane terminal. Config.Env and Spec.Env can still
	// override these values.
	add("TERM=xterm-256color")
	add("COLUMNS=" + strconv.Itoa(int(b.cols)))
	add("LINES=" + strconv.Itoa(int(b.rows)))
	for _, entry := range b.env {
		add(entry)
	}
	for _, entry := range extra {
		add(entry)
	}
	return env
}

func handleID(role string, pid int) string {
	if role == "" {
		role = "headless"
	}
	return role + "-" + strconv.Itoa(pid)
}

// Handle is a running headless child. It implements driver.Handle and
// io.Writer.
type Handle struct {
	backend *Backend
	cmd     *exec.Cmd
	pid     int
	id      string
	paneID  string

	// done closes when the leader is reaped and output draining has finished
	// (possibly truncated). It does not wait for descendants to exit.
	done       chan struct{}
	outputDone chan struct{} // closed when the PTY reader exits
	stopRead   chan struct{} // closed to stop the reader
	stopOnce   sync.Once

	readerMu        sync.Mutex
	readerTruncated bool
	readerErr       error

	// groupGone closes and the handle is removed from the backend only when
	// the whole process group no longer exists.
	groupGone chan struct{}
	groupOnce sync.Once

	waitErr error // valid after done is closed

	stopMu sync.Mutex
	mu     sync.Mutex
	ptmx   *os.File
	closed bool
}

// ID returns "<role>-<pid>".
func (h *Handle) ID() string { return h.id }

// PaneID returns the child PID as a string.
func (h *Handle) PaneID() string { return h.paneID }

// PID returns the child's process ID. Its process group ID is the same value.
func (h *Handle) PID() int { return h.pid }

// Done is closed once the leader has exited and its output has been drained or
// truncated. Descendants may still be running; use Stop/Close to reap them.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Write sends input to the child's PTY. It returns io.ErrClosedPipe after the
// process has exited.
func (h *Handle) Write(p []byte) (int, error) {
	h.mu.Lock()
	f := h.ptmx
	closed := h.closed
	h.mu.Unlock()
	if closed || f == nil {
		return 0, io.ErrClosedPipe
	}
	return writePTY(f, p)
}

// Wait blocks until the leader is reaped and its output has been drained or
// truncated. It is safe to call multiple times, including after Stop. The
// returned error is the result of exec.Cmd.Wait, joined with
// ErrOutputTruncated if a descendant kept the PTY open past the drain timeout.
func (h *Handle) Wait() error {
	<-h.done
	return h.waitErr
}

// Stop terminates the child's process group: SIGTERM, wait for the grace
// period or for the whole group to exit, then SIGKILL. It is idempotent and
// safe for concurrent use.
//
// Liveness is determined by kill(-pgid, 0), not by the leader's Wait: a leader
// may exit while descendants remain. The handle stays registered with the
// backend until the process group is gone, so Stop/Close after leader exit
// still clean up the subtree.
func (h *Handle) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.stopMu.Lock()
	defer h.stopMu.Unlock()

	select {
	case <-h.groupGone:
		return h.waitDoneBounded()
	default:
	}
	if !groupExists(h.pid) {
		h.markGroupGone()
		return h.waitDoneBounded()
	}

	if err := terminateGroup(h.pid); err != nil {
		return fmt.Errorf("headless: SIGTERM process group %d: %w", h.pid, err)
	}

	var ctxErr error
	if h.backend.grace > 0 {
		timer := time.NewTimer(h.backend.grace)
		defer timer.Stop()
		ticker := time.NewTicker(groupPollInterval)
		defer ticker.Stop()

	wait:
		for {
			if !groupExists(h.pid) {
				break
			}
			select {
			case <-h.groupGone:
				break wait
			case <-timer.C:
				break wait
			case <-ctx.Done():
				ctxErr = ctx.Err()
				break wait
			case <-ticker.C:
			}
		}
	}

	if groupExists(h.pid) {
		if err := killGroup(h.pid); err != nil {
			return fmt.Errorf("headless: SIGKILL process group %d: %w", h.pid, err)
		}
		deadline := time.Now().Add(killWaitTimeout)
		for groupExists(h.pid) && time.Now().Before(deadline) {
			time.Sleep(groupPollInterval)
		}
		if groupExists(h.pid) {
			return fmt.Errorf("headless: process group %d still alive after SIGKILL", h.pid)
		}
	}

	h.markGroupGone()
	if err := h.waitDoneBounded(); err != nil {
		return err
	}
	return ctxErr
}

func (h *Handle) waitDoneBounded() error {
	select {
	case <-h.done:
		return nil
	default:
	}
	timeout := h.backend.doneWaitTimeout()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w after %v", ErrWaitTimeout, timeout)
	}
}

func (b *Backend) doneWaitTimeout() time.Duration {
	if d := 2*b.drain + time.Second; d > baseDoneWaitTimeout {
		return d
	}
	return baseDoneWaitTimeout
}

// copyOutput is the single PTY reader. io.Writer has no cancellation, so a
// writer whose Write never returns pins this goroutine (and the master fd)
// until it does. Done/Wait/Stop still return bounded with ErrOutputTruncated;
// callers should use a writer that can return, such as a log file.
func (h *Handle) copyOutput(w io.Writer, ptmx *os.File) {
	truncated, err := pollCopy(w, ptmx, h.stopRead)
	h.readerMu.Lock()
	h.readerTruncated = truncated
	h.readerErr = err
	h.readerMu.Unlock()
	close(h.outputDone)
}

func (h *Handle) wait() {
	leaderErr := h.cmd.Wait()

	truncated := h.drainOutput()
	h.closePTY()

	h.readerMu.Lock()
	if h.readerTruncated {
		truncated = true
	}
	readerErr := h.readerErr
	h.readerMu.Unlock()

	err := leaderErr
	if truncated {
		err = errors.Join(err, ErrOutputTruncated)
	}
	if readerErr != nil && !errors.Is(readerErr, os.ErrClosed) {
		err = errors.Join(err, fmt.Errorf("headless: read pty: %w", readerErr))
	}
	h.waitErr = err
	close(h.done)

	// Keep the handle registered until the entire group is gone. A leader can
	// exit while descendants (which may ignore SIGHUP/SIGTERM) remain.
	if groupExists(h.pid) {
		go h.watchGroup()
	} else {
		h.markGroupGone()
	}
}

// drainOutput waits for the reader to finish after the leader exits. A
// descendant may keep the PTY slave open forever, so after the drain timeout we
// stop the poll-driven reader and report truncation instead of blocking.
func (h *Handle) drainOutput() bool {
	timeout := h.backend.drain
	if timeout <= 0 {
		h.stopOutput()
		return h.waitReader(readerStopTimeout)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.outputDone:
		return h.readerTruncatedNow()
	case <-timer.C:
	}

	h.stopOutput()
	return h.waitReader(readerStopTimeout)
}

func (h *Handle) waitReader(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.outputDone:
		return h.readerTruncatedNow()
	case <-timer.C:
		// Last resort. The poll loop should have exited by now; closing the
		// master releases the fd. Report truncation rather than blocking on a
		// writer that may be wedged.
		h.closePTY()
		return true
	}
}

func (h *Handle) readerTruncatedNow() bool {
	h.readerMu.Lock()
	defer h.readerMu.Unlock()
	return h.readerTruncated
}

func (h *Handle) stopOutput() {
	h.stopOnce.Do(func() { close(h.stopRead) })
}

func (h *Handle) closePTY() {
	h.mu.Lock()
	f := h.ptmx
	h.ptmx = nil
	h.closed = true
	h.mu.Unlock()
	if f != nil {
		_ = f.Close()
	}
}

func (h *Handle) watchContext(ctx context.Context) {
	select {
	case <-h.groupGone:
	case <-ctx.Done():
		// The spawn context is gone; terminate the group with the normal
		// TERM -> grace -> KILL sequence.
		_ = h.Stop(context.Background())
	}
}

// watchGroup removes the handle only once the whole process group is gone.
// This keeps leader-exited groups visible to Kill/Close and prevents the
// backend from forgetting descendants.
func (h *Handle) watchGroup() {
	ticker := time.NewTicker(groupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.groupGone:
			return
		default:
		}
		if !groupExists(h.pid) {
			h.markGroupGone()
			return
		}
		select {
		case <-h.groupGone:
			return
		case <-ticker.C:
		}
	}
}

func (h *Handle) markGroupGone() {
	h.groupOnce.Do(func() {
		h.backend.remove(h)
		close(h.groupGone)
	})
}

func (b *Backend) remove(h *Handle) {
	b.mu.Lock()
	delete(b.handles, h)
	b.mu.Unlock()
}

// lockedWriter serializes writes to a Config-level writer shared by multiple
// spawns. Spec-level writers are the caller's responsibility.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// Unix-only platform helpers: process attributes, PTY polling I/O, and
// process-group operations. Windows is intentionally unsupported.

const (
	pollInterval     = 20 * time.Millisecond
	writePollTimeout = 100 // milliseconds
)

// newSysProcAttr starts the child in a new session with the PTY as its
// controlling terminal. setsid also creates a new process group whose PGID is
// the child PID, so kill(-pid, ...) reaches the whole subtree.
//
// Do not also set Setpgid: on Darwin setsid+setpgid fails with EPERM, and
// setsid already provides the process group we need.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true}
}

func setNonblock(f *os.File) error {
	return unix.SetNonblock(int(f.Fd()), true)
}

func signalGroup(pid int, sig unix.Signal) error {
	err := unix.Kill(-pid, sig)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func terminateGroup(pid int) error { return signalGroup(pid, unix.SIGTERM) }
func killGroup(pid int) error      { return signalGroup(pid, unix.SIGKILL) }

// groupExists reports whether any process (including a zombie leader awaiting
// reaping) remains in the process group. Before sending a signal Stop checks
// this so a recycled PID is not signalled blindly.
func groupExists(pid int) bool {
	err := unix.Kill(-pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// pollCopy reads from a non-blocking PTY master until EOF, an error, or stop.
// The stop channel makes draining bounded even when a descendant keeps the
// slave open: on Darwin, close(master) alone does not reliably wake read(2).
func pollCopy(dst io.Writer, src *os.File, stop <-chan struct{}) (truncated bool, err error) {
	fd := int(src.Fd())
	buf := make([]byte, 32*1024)
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
	for {
		select {
		case <-stop:
			return true, nil
		default:
		}
		pfd[0].Revents = 0
		if _, perr := unix.Poll(pfd, int(pollInterval/time.Millisecond)); perr != nil {
			if errors.Is(perr, unix.EINTR) {
				continue
			}
			return false, perr
		}
		if pfd[0].Revents == 0 {
			continue
		}
		n, rerr := unix.Read(fd, buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return false, werr
			}
			continue
		}
		if rerr != nil {
			if errors.Is(rerr, unix.EINTR) || errors.Is(rerr, unix.EAGAIN) {
				continue
			}
			if errors.Is(rerr, unix.EIO) {
				return false, nil
			}
			return false, rerr
		}
		return false, nil
	}
}

// writePTY writes to a non-blocking PTY master, waiting for writability when
// the kernel buffer is full.
func writePTY(f *os.File, p []byte) (int, error) {
	fd := int(f.Fd())
	total := 0
	for total < len(p) {
		n, err := unix.Write(fd, p[total:])
		if n > 0 {
			total += n
		}
		if err == nil {
			continue
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, perr := unix.Poll(pfd, writePollTimeout); perr != nil && !errors.Is(perr, unix.EINTR) {
				return total, perr
			}
			continue
		}
		return total, err
	}
	return total, nil
}
