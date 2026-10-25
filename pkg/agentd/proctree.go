package agentd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// descendantScanTimeout bounds a single process-table snapshot. Failure is
// best-effort: callers fall back to process-group signalling.
const descendantScanTimeout = 300 * time.Millisecond

// collectDescendants returns all live descendants of rootPID using a ps(1)
// process-table snapshot. The result excludes rootPID and is sorted by PID.
//
// The scan is intentionally best-effort: it uses /bin/ps when available and
// falls back to "ps" on PATH. Callers must degrade to process-group signals
// when it returns an error.
func collectDescendants(rootPID int) ([]int, error) {
	return collectDescendantsWithin(rootPID, time.Now().Add(descendantScanTimeout))
}

// collectDescendantsWithin is collectDescendants with an explicit deadline so
// shutdown can keep the total 2s+1s budget even when ps(1) is slow. A single
// snapshot is also capped at descendantScanTimeout, so one slow ps cannot
// consume an entire grace phase.
func collectDescendantsWithin(rootPID int, deadline time.Time) ([]int, error) {
	// PID 1 is init/launchd: scanning it would collect the whole system, and
	// signalling -1 has process-group-wide semantics. Refuse unsafe roots.
	if rootPID <= 1 {
		return nil, fmt.Errorf("refusing to collect descendants of unsafe pid %d", rootPID)
	}

	now := time.Now()
	if deadline.IsZero() || now.Add(descendantScanTimeout).Before(deadline) {
		deadline = now.Add(descendantScanTimeout)
	}
	if !now.Before(deadline) {
		return nil, context.DeadlineExceeded
	}

	ps, err := psBinary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	out, err := exec.CommandContext(ctx, ps, "-axo", "pid=,ppid=").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("run %s -axo: %w", ps, err)
		}
		// BusyBox ps variants may not support -axo. Retry the POSIX-ish
		// spelling before callers degrade to process-group signalling.
		out, err = exec.CommandContext(ctx, ps, "-A", "-o", "pid=,ppid=").Output()
		if err != nil {
			return nil, fmt.Errorf("run %s -A -o pid=,ppid=: %w", ps, err)
		}
	}

	children := parseProcessTable(out)
	seen := map[int]bool{rootPID: true}
	stack := append([]int(nil), children[rootPID]...)
	var descendants []int
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		descendants = append(descendants, pid)
		stack = append(stack, children[pid]...)
	}
	sort.Ints(descendants)
	return descendants, nil
}

// parseProcessTable tolerates blank lines, headers, and malformed rows. It
// returns a parent-PID -> child-PID index from "pid ppid" pairs.
func parseProcessTable(out []byte) map[int][]int {
	children := make(map[int][]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPID != nil || errPPID != nil || pid <= 0 || ppid < 0 {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children
}

var (
	psBinaryOnce sync.Once
	psBinaryPath string
	psBinaryErr  error
)

// psBinary prefers the stable absolute /bin/ps and only falls back to PATH
// when it is missing.
func psBinary() (string, error) {
	psBinaryOnce.Do(func() {
		if _, err := os.Stat("/bin/ps"); err == nil {
			psBinaryPath = "/bin/ps"
			return
		}
		psBinaryPath, psBinaryErr = exec.LookPath("ps")
		if psBinaryErr != nil {
			psBinaryErr = fmt.Errorf("locate ps: %w", psBinaryErr)
		}
	})
	return psBinaryPath, psBinaryErr
}
