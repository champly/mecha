// Package core manages multiple role agents, their terminal panes, and
// dispatches tasks in response to ask requests.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"

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
	handle term.PaneHandle
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
	backend   term.PaneBackend

	coordinator       types.Agent
	specialists       map[string]*instance
	agentByID         map[string]types.Agent
	instanceByAgentID map[string]*instance

	logger *slog.Logger
}

// New creates a Core for the given workspace and config.
func New(workspace string, cfg config.Config) (*Core, error) {
	backend, err := term.NewAutoProvider()
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}

	logger, err := initLogger(workspace)
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
	}, nil
}

// Start launches the HTTP server and the coordinator agent.
func (c *Core) Start(ctx context.Context) error {
	roleName := c.coordinatorRole()
	if roleName == "" {
		return fmt.Errorf("core: no coordinator role found in profile %q", c.cfg.Profile)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("core: listen: %w", err)
	}
	config.WebhookPort = fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
	c.logger.Info("http server listening", "addr", "127.0.0.1:"+config.WebhookPort)

	srv := c.startHTTPServer(ln)

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
