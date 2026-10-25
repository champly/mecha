package agentd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCollectDescendants(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentdProcessTreeHelper$")
	cmd.Env = append(os.Environ(),
		"AGENTD_TREE_DEPTH=3",
		"AGENTD_TREE_PID_DIR="+dir,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process tree: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	root := cmd.Process.Pid
	want := []int{
		waitPIDFile(t, filepath.Join(dir, "depth-3.pid")),
		waitPIDFile(t, filepath.Join(dir, "depth-2.pid")),
		waitPIDFile(t, filepath.Join(dir, "depth-1.pid")),
	}

	// Use a relaxed overall deadline; the production 300ms per-snapshot cap
	// still applies inside collectDescendantsWithin.
	got, err := collectDescendantsWithin(root, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatalf("collectDescendantsWithin(%d): %v", root, err)
	}
	if !sort.IntsAreSorted(got) {
		t.Errorf("descendants %v are not sorted", got)
	}
	wantSet := make(map[int]bool, len(want))
	for _, pid := range want {
		wantSet[pid] = true
	}
	gotSet := make(map[int]bool, len(got))
	for _, pid := range got {
		if pid == root {
			t.Errorf("descendants include root %d", root)
		}
		if gotSet[pid] {
			t.Errorf("descendant %d appears more than once in %v", pid, got)
		}
		gotSet[pid] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Errorf("descendants = %v, want exactly %v", got, want)
	}
	for pid := range wantSet {
		if !gotSet[pid] {
			t.Errorf("descendant %d missing from %v", pid, got)
		}
	}
	for pid := range gotSet {
		if !wantSet[pid] {
			t.Errorf("unexpected descendant %d in %v", pid, got)
		}
	}
}

func TestCollectDescendantsRejectsUnsafeRoot(t *testing.T) {
	for _, pid := range []int{-1, 0, 1} {
		if _, err := collectDescendantsWithin(pid, time.Now().Add(time.Second)); err == nil {
			t.Errorf("collectDescendantsWithin(%d) = nil error, want rejection", pid)
		}
		if _, err := collectDescendants(pid); err == nil {
			t.Errorf("collectDescendants(%d) = nil error, want rejection", pid)
		}
	}
}

func TestParseProcessTableToleratesBadRows(t *testing.T) {
	out := []byte("  1     0\n 10     1\n20 10\n\nPID PPID\nnot a pid\n30 -1\n40 20\n")
	children := parseProcessTable(out)

	if got := children[1]; len(got) != 1 || got[0] != 10 {
		t.Errorf("children[1] = %v, want [10]", got)
	}
	if got := children[10]; len(got) != 1 || got[0] != 20 {
		t.Errorf("children[10] = %v, want [20]", got)
	}
	if got := children[20]; len(got) != 1 || got[0] != 40 {
		t.Errorf("children[20] = %v, want [40]", got)
	}
	if _, ok := children[-1]; ok {
		t.Error("negative PPID row was not ignored")
	}
}

// TestAgentdProcessTreeHelper is re-executed as a multi-level child tree.
func TestAgentdProcessTreeHelper(t *testing.T) {
	depthStr := os.Getenv("AGENTD_TREE_DEPTH")
	if depthStr == "" {
		return
	}
	depth, err := strconv.Atoi(depthStr)
	if err != nil {
		os.Exit(2)
	}
	if depth <= 0 {
		for {
			time.Sleep(time.Hour)
		}
	}

	child := exec.Command(os.Args[0], "-test.run=^TestAgentdProcessTreeHelper$")
	child.Env = append(os.Environ(), "AGENTD_TREE_DEPTH="+strconv.Itoa(depth-1))
	child.Stdin, child.Stdout, child.Stderr = nil, nil, nil
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if dir := os.Getenv("AGENTD_TREE_PID_DIR"); dir != "" {
		file := filepath.Join(dir, fmt.Sprintf("depth-%d.pid", depth))
		if err := os.WriteFile(file, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			os.Exit(2)
		}
	}
	_ = child.Wait()
}

// TestAgentdSetsidHelper is re-executed as the fake agent leader. It starts a
// child in an independent session, then waits, mirroring an agent tool that
// escapes its process group with setsid(2).
func TestAgentdSetsidHelper(t *testing.T) {
	if os.Getenv("AGENTD_SETSID_HELPER") != "1" {
		return
	}
	childFile := os.Getenv("CHILD_FILE")
	if childFile == "" {
		os.Exit(2)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestAgentdSleepHelper$")
	child.Env = append(os.Environ(), "AGENTD_SLEEP_HELPER=1")
	child.Stdin, child.Stdout, child.Stderr = nil, nil, nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(childFile, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
		os.Exit(2)
	}
	_ = child.Wait()
}

// TestAgentdSleepHelper is the independent-session descendant. It must be
// cleaned up by agentd's PID-level termination, not by its parent's group.
func TestAgentdSleepHelper(t *testing.T) {
	if os.Getenv("AGENTD_SLEEP_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}
