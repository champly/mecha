// Package core manages multiple role agents, their terminal panes, and
// dispatches tasks in response to ask requests.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
)

const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusBusy     = "busy"
)

type instance struct {
	role   string
	agent  types.Agent
	handle term.Handle
	status string          // starting | running | busy
	ready  chan struct{}   // closed when SessionStart arrives
	result chan taskResult // per-task completion signal
}

type taskResult struct {
	output string
	err    string
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

	return &Core{
		workspace:         workspace,
		cfg:               cfg,
		backend:           backend,
		specialists:       make(map[string]*instance),
		agentByID:         make(map[string]types.Agent),
		instanceByAgentID: make(map[string]*instance),
		logger:            logger,
		logFile:           logFile,
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
