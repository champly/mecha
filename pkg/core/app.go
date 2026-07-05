// Package core manages multiple role agents, their terminal panes, and
// dispatches tasks in response to ask requests.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
)

const (
	statusStarting int32 = iota + 1
	statusRunning
	statusBusy
)

type instance struct {
	role      string
	agent     types.Agent
	handle    term.Handle
	status    atomic.Int32
	ready     chan struct{} // closed when startup completes (success or failure)
	readyOnce sync.Once
	startErr  error
	taskSlot  chan struct{}
	result    atomic.Pointer[chan taskResult] // per-task completion signal
}

type taskResult struct {
	output string
	err    string
}

func (inst *instance) signalReady() {
	inst.readyOnce.Do(func() {
		close(inst.ready)
	})
}

func (inst *instance) waitReady(ctx context.Context) error {
	if inst.status.Load() != statusStarting {
		return nil
	}

	select {
	case <-inst.ready:
		return inst.startErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (inst *instance) beginTask(ctx context.Context) error {
	select {
	case <-inst.taskSlot:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (inst *instance) finishTask() {
	inst.taskSlot <- struct{}{}
}

// Core manages the lifecycle of all role agents in a workspace.
type Core struct {
	workspace string
	cfg       config.Config
	runtime   config.Runtime
	backend   term.Backend

	coordinator       types.Agent
	specialists       map[string]*instance
	agentByID         map[string]types.Agent
	instanceByAgentID map[string]*instance

	// mu guards concurrent access to the following fields:
	// specialists, agentByID, instanceByAgentID, and instance.status/result.
	mu sync.Mutex

	shutdownGracePeriod time.Duration

	logger  *slog.Logger
	logFile *os.File
}

// New creates a Core for the given workspace and config.
func New(workspace string, cfg config.Config) (*Core, error) {
	backend, err := term.New()
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}

	logger, logFile, err := initLogger(workspace)
	if err != nil {
		return nil, fmt.Errorf("core: init logger: %w", err)
	}

	gracePeriod := 30 * time.Second
	if cfg.ShutdownGracePeriod != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(cfg.ShutdownGracePeriod)); err == nil {
			gracePeriod = d
		} else {
			slog.Warn("core: invalid shutdown_grace_period, using default 30s", "value", cfg.ShutdownGracePeriod, "err", err)
		}
	}

	return &Core{
		workspace:           workspace,
		cfg:                 cfg,
		backend:             backend,
		specialists:         make(map[string]*instance),
		agentByID:           make(map[string]types.Agent),
		instanceByAgentID:   make(map[string]*instance),
		shutdownGracePeriod: gracePeriod,
		logger:              logger,
		logFile:             logFile,
	}, nil
}

// Close releases resources held by Core (log file, etc.).
func (c *Core) Close() error {
	if c.logFile != nil {
		return c.logFile.Close()
	}
	return nil
}

// Start launches the HTTP server and the coordinator agent.
func (c *Core) Start(ctx context.Context) error {
	defer c.Close()

	roleName := c.coordinatorRole()
	if roleName == "" {
		return fmt.Errorf("core: no coordinator role found in profile %q", c.cfg.Profile)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("core: listen: %w", err)
	}
	port := fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
	c.logger.Info("http server listening", "addr", "127.0.0.1:"+port)

	srv := c.startHTTPServer(ln)

	c.runtime = config.Runtime{
		MechaBinary: config.MechaBinary,
		WebhookPort: port,
	}
	a, err := c.createAgent(roleName)
	if err != nil {
		return err
	}
	c.coordinator = a

	return c.launchCoordinator(ctx, a, srv)
}

func (c *Core) coordinatorRole() string {
	for _, r := range c.cfg.Profiles[c.cfg.Profile].Roles {
		if r.IsCoordinator {
			return r.Name
		}
	}
	return ""
}
