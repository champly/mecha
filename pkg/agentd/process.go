package agentd

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/champly/mecha/pkg/agent"
	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// startAgent creates the agent, prepares its role directory, and launches it
// with a PTY attached to agentd's stdio.
func (a *Agentd) startAgent(cfg *api.RegisterResponse, webhookAddr string) error {
	ag, err := agent.NewFromConfig(
		cfg.Workspace,
		cfg.Prompt,
		cfg.RoleName,
		webhookAddr,
		config.AgentConfig{
			Type:   cfg.Agent.Type,
			Binary: cfg.Agent.Binary,
			Model:  cfg.Agent.Model,
			Params: cfg.Agent.Params.AsMap(),
			Envs:   cfg.Agent.Envs,
		},
		cfg.MechaBinary,
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if err := ag.Prepare(); err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	a.webhook.SetParseFunc(ag.ParseHookEvent)

	cmd := ag.Cmd()
	ptmx, err := launchPTY(cmd)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.agentType = cfg.Agent.Type
	a.ptmx = ptmx
	a.cmd = cmd
	// Remember the process-group ID even after waitAgent clears cmd: a leader
	// can exit while descendants remain, and Close must still reap them.
	a.agentPID = cmd.Process.Pid
	a.mu.Unlock()

	restore := makeRawIfTerminal()

	out := &activityWriter{w: os.Stdout, last: &a.lastOutput}
	go io.Copy(out, ptmx)
	go a.forwardStdin()
	go a.watchWinch()
	go a.watchReady()
	go a.waitAgent(cmd, restore)

	return nil
}

// forwardStdin copies stdin to the PTY until shutdown. os.Stdin.Read blocks,
// so a stray goroutine can outlive signalStop; that is harmless.
func (a *Agentd) forwardStdin() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		select {
		case <-a.stop:
			return
		default:
		}
		a.mu.Lock()
		ptmx := a.ptmx
		a.mu.Unlock()
		if ptmx == nil {
			return
		}
		if _, err := ptmx.Write(buf[:n]); err != nil {
			return
		}
	}
}

// activityWriter records the time of each write, used to detect when the
// agent's output goes quiet.
type activityWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.last.Store(time.Now().UnixNano())
	return a.w.Write(p)
}

const (
	readyQuietPeriod = 1500 * time.Millisecond
	readyTimeout     = 30 * time.Second
)

// watchReady closes a.ready once the agent's TUI has finished its initial
// render. Tasks written earlier land in the input box but lose the enter key.
func (a *Agentd) watchReady() {
	defer close(a.ready)

	deadline := time.Now().Add(readyTimeout)
	quiet := false
	for time.Now().Before(deadline) {
		last := a.lastOutput.Load()
		if last != 0 && time.Since(time.Unix(0, last)) > readyQuietPeriod {
			quiet = true
			break
		}
		select {
		case <-a.stop:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !quiet {
		slog.Warn("agent readiness wait timed out; tasks will be written anyway", "id", a.opts.ID)
		return
	}

	// A quiet window can fall inside a pause between initialization steps, so
	// wait a bit longer before declaring readiness.
	select {
	case <-a.stop:
	case <-time.After(500 * time.Millisecond):
	}
}

// launchPTY starts cmd with a PTY and returns the PTY master.
func launchPTY(cmd *exec.Cmd) (*os.File, error) {
	sz, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		sz = &pty.Winsize{Rows: 24, Cols: 80}
	}

	ptmx, err := pty.StartWithSize(cmd, sz)
	if err != nil {
		return nil, fmt.Errorf("start with pty: %w", err)
	}
	time.Sleep(time.Millisecond * 100) // give the agent a moment to initialize its TUI

	return ptmx, nil
}

// makeRawIfTerminal switches stdin to raw mode when it is a terminal, so
// keystrokes reach the agent's TUI unmodified. It returns a restore function.
func makeRawIfTerminal() func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}
	return func() { _ = term.Restore(fd, oldState) }
}

// waitAgent is the only owner of cmd.Wait. It waits for the agent to exit,
// restores the terminal, fails any in-flight task, then closes the PTY and
// signals shutdown.
func (a *Agentd) waitAgent(cmd *exec.Cmd, restore func()) {
	defer func() {
		if a.agentDone != nil {
			close(a.agentDone)
		}
	}()

	if err := cmd.Wait(); err != nil {
		slog.Warn("agent exited", "id", a.opts.ID, "err", err)
	}
	restore()

	if r, ok := a.takeTaskResult(taskResult{result: "agent exited during task"}); ok {
		a.taskCh <- r
	}

	a.mu.Lock()
	ptmx := a.ptmx
	a.ptmx = nil
	a.cmd = nil
	a.mu.Unlock()
	if ptmx != nil {
		_ = ptmx.Close()
	}
	a.signalStop()
}

const (
	// Agent shutdown must finish inside Core's pane budget. Core's minimum
	// ShutdownTimeout is 5s, which covers 2s TERM grace + 1s post-SIGKILL
	// wait + 2s reserve for scheduling and PTY draining.
	defaultAgentStopGrace = 2 * time.Second
	defaultAgentKillWait  = 1 * time.Second
	agentGroupPoll        = 20 * time.Millisecond
	// descendantRescanInterval bounds how often shutdown forks ps(1) while
	// looking for descendants that left the agent's process group.
	descendantRescanInterval = 150 * time.Millisecond
)

// Overridable in tests to keep process-group termination fast.
var (
	agentStopGrace = defaultAgentStopGrace
	agentKillWait  = defaultAgentKillWait
)

// stopAgent terminates the agent's process group with SIGTERM, waits for the
// grace period, then escalates to SIGKILL. It uses the remembered process-group
// ID, so a leader that already exited does not hide surviving descendants.
//
// waitAgent owns cmd.Wait; this method only waits (bounded) for its bookkeeping
// to finish and never reaps the process itself.
func (a *Agentd) stopAgent() {
	a.mu.Lock()
	pid := a.agentPID
	cmd := a.cmd
	a.mu.Unlock()

	if pid == 0 && cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	if pid > 1 {
		// Share one 2s+1s budget across group termination and leader
		// bookkeeping. The old code waited agentKillWait twice (once in
		// terminateAgentGroup and again here), which could consume 4s before
		// webhook/conn teardown and exceed Core's 5s pane budget.
		stopDeadline := time.Now().Add(agentStopGrace + agentKillWait)
		a.terminateAgentGroup(pid)
		if a.agentDone != nil {
			remaining := time.Until(stopDeadline)
			if remaining < 0 {
				remaining = 0
			}
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-a.agentDone:
			case <-timer.C:
				slog.Warn("agentd: timed out waiting for agent process exit", "id", a.opts.ID)
			}
		}
	}

	a.mu.Lock()
	ptmx := a.ptmx
	a.ptmx = nil
	a.cmd = nil
	a.mu.Unlock()
	if ptmx != nil {
		_ = ptmx.Close()
	}
}

// terminateAgentGroup terminates both the agent process group and any
// descendants that escaped it with setsid(2), sharing one 2s+1s budget.
//
// The process group covers normal agents and shell/tool children. Some tools
// deliberately start their command in a new session, which kill(-pgid) cannot
// reach, so shutdown also takes a best-effort ps(1) snapshot and signals those
// PIDs individually. The scan is repeated during the grace period; a failed
// scan is logged and degrades to the historical group-only behavior.
func (a *Agentd) terminateAgentGroup(pid int) {
	if pid <= 1 {
		return
	}
	started := time.Now()
	graceDeadline := started.Add(agentStopGrace)
	killDeadline := started.Add(agentStopGrace + agentKillWait)

	// Snapshot before TERM so a descendant that leaves the group or is
	// reparented after the signal is still targeted by PID.
	tracked := make(map[int]struct{})
	scanWarned := false
	fresh := a.scanAgentDescendants(pid, graceDeadline, tracked, "initial", &scanWarned)
	signalPIDs(a.opts.ID, fresh, syscall.SIGTERM)

	if agentGroupExists(pid) {
		if err := signalAgentGroup(pid, syscall.SIGTERM); err != nil {
			slog.Warn("agentd: SIGTERM agent group", "id", a.opts.ID, "pgid", pid, "err", err)
		}
	}

	if waitAgentTreeGone(pid, tracked, syscall.SIGTERM, graceDeadline, true, a.opts.ID, &scanWarned) {
		return
	}

	// Re-scan for anything spawned during the grace period, then escalate.
	// Newly discovered PIDs land in tracked, which we signal below.
	a.scanAgentDescendants(pid, killDeadline, tracked, "pre-SIGKILL", &scanWarned)
	signalPIDs(a.opts.ID, trackedPIDs(tracked), syscall.SIGKILL)
	if agentGroupExists(pid) {
		if err := signalAgentGroup(pid, syscall.SIGKILL); err != nil {
			slog.Warn("agentd: SIGKILL agent group", "id", a.opts.ID, "pgid", pid, "err", err)
		}
	}
	if !waitAgentTreeGone(pid, tracked, syscall.SIGKILL, killDeadline, false, a.opts.ID, &scanWarned) {
		slog.Warn("agentd: agent tree still alive after SIGKILL", "id", a.opts.ID, "pgid", pid, "tracked", len(tracked))
	}
}

// scanAgentDescendants merges a fresh ps(1) snapshot into tracked and returns
// only the newly discovered PIDs. Errors are logged and degraded by callers.
func (a *Agentd) scanAgentDescendants(rootPID int, deadline time.Time, tracked map[int]struct{}, phase string, scanWarned *bool) []int {
	fresh, err := mergeDescendants(rootPID, deadline, tracked)
	if err != nil {
		if scanWarned == nil || !*scanWarned {
			slog.Warn("agentd: scan agent descendants; using process-group signalling only",
				"id", a.opts.ID, "pgid", rootPID, "phase", phase, "err", err)
			if scanWarned != nil {
				*scanWarned = true
			}
		}
		return nil
	}
	if len(fresh) > 0 {
		slog.Info("agentd: collected agent descendants",
			"id", a.opts.ID, "pgid", rootPID, "phase", phase, "new", len(fresh), "total", len(tracked))
	} else {
		slog.Debug("agentd: no new agent descendants",
			"id", a.opts.ID, "pgid", rootPID, "phase", phase, "total", len(tracked))
	}
	return fresh
}

// mergeDescendants adds descendants not yet in tracked to it, returning the
// new PIDs. Each ps(1) snapshot is limited to min(deadline, now+300ms), so a
// slow scan cannot consume the whole grace phase. Only PIDs observed in a
// snapshot are ever signalled; see waitAgentTreeGone for the PID-reuse caveat.
func mergeDescendants(rootPID int, deadline time.Time, tracked map[int]struct{}) ([]int, error) {
	scanDeadline := deadline
	if capDeadline := time.Now().Add(descendantScanTimeout); scanDeadline.IsZero() || capDeadline.Before(scanDeadline) {
		scanDeadline = capDeadline
	}
	descendants, err := collectDescendantsWithin(rootPID, scanDeadline)
	if err != nil {
		return nil, err
	}
	ownPID := os.Getpid()
	fresh := make([]int, 0, len(descendants))
	for _, pid := range descendants {
		if pid <= 1 || pid == ownPID {
			continue
		}
		if _, ok := tracked[pid]; ok {
			continue
		}
		tracked[pid] = struct{}{}
		fresh = append(fresh, pid)
	}
	return fresh, nil
}

// signalPIDs delivers sig to each PID individually. ESRCH is expected for
// processes that exited between the snapshot and the signal.
func signalPIDs(id string, pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if pid <= 1 || pid == os.Getpid() {
			continue
		}
		if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			slog.Warn("agentd: signal agent descendant",
				"id", id, "pid", pid, "signal", sig, "err", err)
		}
	}
}

func trackedPIDs(tracked map[int]struct{}) []int {
	pids := make([]int, 0, len(tracked))
	for pid := range tracked {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func anyProcessAlive(tracked map[int]struct{}) bool {
	for pid := range tracked {
		if processAlive(pid) {
			return true
		}
	}
	return false
}

// waitAgentTreeGone waits until both the process group and every tracked
// descendant are gone, or deadline passes. During the grace phase it rescans
// periodically and signals newly discovered descendants, plus one final scan
// when the tree first appears empty so a late setsid child is not missed.
//
// PID reuse caveat: tracked PIDs are retained through the SIGKILL phase, so a
// PID that exits and is reused by an unrelated process can stay in the set for
// roughly agentStopGrace+agentKillWait (~3s by default) and be signalled. Only
// PIDs observed in a recent descendant snapshot are tracked, but this remains
// an accepted best-effort risk.
func waitAgentTreeGone(rootPID int, tracked map[int]struct{}, sig syscall.Signal, deadline time.Time, rescan bool, id string, scanWarned *bool) bool {
	ticker := time.NewTicker(agentGroupPoll)
	defer ticker.Stop()

	nextScan := time.Now().Add(descendantRescanInterval)
	finalScanDone := false

	scan := func() {
		fresh, err := mergeDescendants(rootPID, deadline, tracked)
		if err != nil {
			if scanWarned == nil || !*scanWarned {
				slog.Warn("agentd: rescan agent descendants", "id", id, "pgid", rootPID, "err", err)
				if scanWarned != nil {
					*scanWarned = true
				}
			}
			return
		}
		if len(fresh) > 0 {
			slog.Info("agentd: discovered new agent descendants",
				"id", id, "pgid", rootPID, "signal", sig, "count", len(fresh))
			signalPIDs(id, fresh, sig)
		}
	}

	for {
		if !agentGroupExists(rootPID) && !anyProcessAlive(tracked) {
			if !rescan || finalScanDone {
				return true
			}
			// One last look before declaring the tree empty.
			finalScanDone = true
			scan()
		}

		now := time.Now()
		if !now.Before(deadline) {
			return false
		}
		if rescan && !finalScanDone && !now.Before(nextScan) {
			scan()
			nextScan = now.Add(descendantRescanInterval)
		}
		<-ticker.C
	}
}

// signalAgentGroup signals every process in the group led by pid. The agent is
// started with Setsid, so its PGID equals its PID. ESRCH means the group is
// already gone and is not an error.
func signalAgentGroup(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// agentGroupExists reports whether any process remains in the group. A zombie
// leader awaiting reaping counts as present, which is why waitAgent must run
// concurrently.
func agentGroupExists(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// watchWinch forwards SIGWINCH to the PTY.
func (a *Agentd) watchWinch() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-a.stop:
			return
		case <-sigCh:
			a.mu.Lock()
			ptmx := a.ptmx
			a.mu.Unlock()
			if ptmx == nil {
				return
			}
			sz, err := pty.GetsizeFull(os.Stdin)
			if err != nil {
				continue
			}
			pty.Setsize(ptmx, sz)
		}
	}
}
