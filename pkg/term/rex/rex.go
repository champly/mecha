// Package rex provides a driver.Backend backed by the Rex terminal CLI.
package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/champly/mecha/pkg/term/driver"
)

const prefix = "rex"

// Rex is a driver.Backend backed by the rex CLI.
type Rex struct {
	mu       sync.Mutex
	binary   string
	server   string
	anchorID string
	blocks   driver.Chain
	run      func(context.Context, string, []string, []string) (string, error)
}

type splitResult struct {
	BlockIDs []string `json:"block_ids"`
}

// New creates a Rex provider pinned to the terminal running this process.
// Rex only guarantees REX_BLOCK for every terminal; the block implies its
// session. REX_SERVER is optional and the CLI locates the server when absent.
func New() (driver.Backend, error) {
	anchorID := os.Getenv("REX_BLOCK")
	if anchorID == "" {
		err := errors.New("term/rex: REX_BLOCK is empty")
		return nil, fmt.Errorf("%w: %w", driver.ErrAnchorUnavailable, err)
	}

	binary, err := resolveBinary()
	if err != nil {
		return nil, fmt.Errorf("term/rex: %w", err)
	}
	return &Rex{
		binary:   binary,
		server:   os.Getenv("REX_SERVER"),
		anchorID: anchorID,
		run:      runRex,
	}, nil
}

// Match reports whether this process is running in a Rex terminal and the rex
// CLI can be found.
func Match() bool {
	if os.Getenv("REX_BLOCK") == "" {
		return false
	}
	_, err := resolveBinary()
	return err == nil
}

func (r *Rex) runner() func(context.Context, string, []string, []string) (string, error) {
	if r.run != nil {
		return r.run
	}
	return runRex
}

func (r *Rex) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.anchorID == "" {
		return nil, errors.New("term/rex: empty anchor block")
	}

	direction := "below"
	target := r.blocks.Last()
	if target == "" {
		direction = "right"
		target = r.anchorID
	}

	args := r.splitArgs(target, direction, spec)
	out, err := r.runner()(ctx, r.binary, args, spec.Env)
	if err != nil {
		return nil, err
	}
	blockID, err := parseBlockID(out)
	if err != nil {
		return nil, err
	}

	r.blocks.Push(blockID)
	return driver.NewHandle(prefix, blockID), nil
}

func (r *Rex) splitArgs(target, direction string, spec driver.Spec) []string {
	args := r.withServer("split")
	args = append(args,
		"-b", target,
		"--split="+direction,
		"--focus=false",
		"--json",
	)
	if spec.WorkDir != "" {
		args = append(args, "--cwd", spec.WorkDir)
	}
	if len(spec.Command) > 0 {
		args = append(args, "--shell=none", "--")
		args = append(args, spec.Command...)
	}
	return args
}

// withServer returns the subcommand followed by -S when REX_SERVER is known.
func (r *Rex) withServer(subcmd ...string) []string {
	args := append([]string(nil), subcmd...)
	if r.server != "" {
		args = append(args, "-S", r.server)
	}
	return args
}

func parseBlockID(out string) (string, error) {
	var result splitResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		return "", fmt.Errorf("term/rex: decode split result: %w", err)
	}
	if len(result.BlockIDs) == 0 || result.BlockIDs[0] == "" {
		return "", errors.New("term/rex: split result has no block id")
	}
	return result.BlockIDs[0], nil
}

// Kill closes a block and drops it from the split chain even on failure.
func (r *Rex) Kill(ctx context.Context, h driver.Handle) error {
	if h == nil || h.PaneID() == "" {
		return errors.New("term/rex: empty block id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	args := append(r.withServer("block", "close"), h.PaneID())
	_, err := r.runner()(ctx, r.binary, args, nil)
	r.blocks.Remove(h.PaneID())
	return err
}

// Close implements driver.Backend; Rex holds no persistent resources.
func (r *Rex) Close() error {
	return nil
}

func resolveBinary() (string, error) {
	if dir := os.Getenv("REX_BIN_DIR"); dir != "" {
		if path, err := exec.LookPath(filepath.Join(dir, "rex")); err == nil {
			return path, nil
		}
	}
	path, err := exec.LookPath("rex")
	if err != nil {
		return "", errors.New("rex CLI not found in REX_BIN_DIR or PATH")
	}
	return path, nil
}

func runRex(ctx context.Context, binary string, args, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("term/rex: rex %s failed: %w: %s", strings.Join(args, " "), err, detail)
	}
	return string(out), nil
}
