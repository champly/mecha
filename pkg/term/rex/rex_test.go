package rex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/champly/mecha/pkg/term/driver"
)

func TestSpawnUsesPinnedAnchorAndSplitsDown(t *testing.T) {
	type call struct {
		binary string
		args   []string
		env    []string
	}
	var calls []call
	run := func(_ context.Context, binary string, args, env []string) (string, error) {
		calls = append(calls, call{binary: binary, args: append([]string(nil), args...), env: append([]string(nil), env...)})
		switch len(calls) {
		case 1:
			return `{"block_ids":["block:child-1"]}`, nil
		case 2:
			return `{"block_ids":["block:child-2"]}`, nil
		default:
			t.Fatalf("unexpected call %d", len(calls))
			return "", nil
		}
	}

	r := &Rex{
		binary:    "/opt/rex/bin/rex",
		server:    "unix:///tmp/rex.sock",
		sessionID: "session:work",
		anchorID:  "block:coordinator",
		run:       run,
	}
	env := []string{"MECHA_ROLE=architect"}
	h1, err := r.Spawn(context.Background(), driver.Spec{
		WorkDir: "/tmp/work dir",
		Command: []string{"/bin/mecha", "agentd", "--dir", "/tmp/work dir"},
		Env:     env,
	})
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	if got := h1.PaneID(); got != "block:child-1" {
		t.Fatalf("first pane id = %q, want block:child-1", got)
	}

	h2, err := r.Spawn(context.Background(), driver.Spec{Command: []string{"true"}})
	if err != nil {
		t.Fatalf("second Spawn: %v", err)
	}
	if got := h2.PaneID(); got != "block:child-2" {
		t.Fatalf("second pane id = %q, want block:child-2", got)
	}
	if len(calls) != 2 {
		t.Fatalf("rex calls = %d, want 2", len(calls))
	}

	wantFirst := []string{
		"-S", "unix:///tmp/rex.sock",
		"split",
		"-s", "session:work",
		"-b", "block:coordinator",
		"--split=right",
		"--focus=false",
		"--json",
		"--cwd", "/tmp/work dir",
		"--shell=none",
		"--",
		"/bin/mecha", "agentd", "--dir", "/tmp/work dir",
	}
	if !slices.Equal(calls[0].args, wantFirst) {
		t.Errorf("first args = %#v, want %#v", calls[0].args, wantFirst)
	}
	if !slices.Equal(calls[0].env, env) {
		t.Errorf("first env = %#v, want %#v", calls[0].env, env)
	}
	if got := calls[0].binary; got != "/opt/rex/bin/rex" {
		t.Errorf("binary = %q, want /opt/rex/bin/rex", got)
	}

	wantSecond := []string{
		"-S", "unix:///tmp/rex.sock",
		"split",
		"-s", "session:work",
		"-b", "block:child-1",
		"--split=below",
		"--focus=false",
		"--json",
		"--shell=none",
		"--",
		"true",
	}
	if !slices.Equal(calls[1].args, wantSecond) {
		t.Errorf("second args = %#v, want %#v", calls[1].args, wantSecond)
	}
}

func TestSpawnRejectsInvalidResult(t *testing.T) {
	tests := []struct {
		name string
		out  string
	}{
		{"invalid json", "not json"},
		{"no blocks", `{"block_ids":[]}`},
		{"empty block", `{"block_ids":[""]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Rex{
				binary:    "rex",
				server:    "local",
				sessionID: "session:work",
				anchorID:  "block:coordinator",
				run: func(context.Context, string, []string, []string) (string, error) {
					return tt.out, nil
				},
			}
			if _, err := r.Spawn(context.Background(), driver.Spec{Command: []string{"true"}}); err == nil {
				t.Fatal("Spawn: expected error, got nil")
			}
			if !r.blocks.Empty() {
				t.Fatal("failed Spawn added a block to the chain")
			}
		})
	}
}

func TestKillClosesBlockAndRemovesChainEntry(t *testing.T) {
	var got []string
	r := &Rex{
		binary:    "rex",
		server:    "local",
		sessionID: "session:work",
		blocks: func() driver.Chain {
			var chain driver.Chain
			chain.Push("block:child")
			return chain
		}(),
		run: func(_ context.Context, _ string, args, _ []string) (string, error) {
			got = append([]string(nil), args...)
			return "", errors.New("close failed")
		},
	}

	err := r.Kill(context.Background(), driver.NewHandle("test", "block:child"))
	if err == nil {
		t.Fatal("Kill: expected close error, got nil")
	}
	want := []string{"-S", "local", "block", "close", "-s", "session:work", "block:child"}
	if !slices.Equal(got, want) {
		t.Errorf("Kill args = %#v, want %#v", got, want)
	}
	if !r.blocks.Empty() {
		t.Fatal("Kill did not remove the block from the chain")
	}
}

func TestKillRejectsEmptyBlockID(t *testing.T) {
	r := &Rex{}
	if err := r.Kill(context.Background(), driver.NewHandle("test", "")); err == nil {
		t.Fatal("Kill with empty block id: expected error, got nil")
	}
}

func TestNewRequiresRexEnvironment(t *testing.T) {
	t.Setenv("REX_SERVER", "")
	t.Setenv("REX_SESSION", "")
	t.Setenv("REX_BLOCK", "")
	if _, err := New(); !errors.Is(err, driver.ErrAnchorUnavailable) {
		t.Fatalf("New without Rex environment = %v, want ErrAnchorUnavailable", err)
	}
}

func TestResolveBinaryUsesRexBinDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_BIN_DIR", dir)

	got, err := resolveBinary()
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if got != path {
		t.Errorf("resolveBinary = %q, want %q", got, path)
	}
}
