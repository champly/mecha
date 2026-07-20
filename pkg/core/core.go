// Package core runs the orchestration gRPC service and manages agentd instances.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/term"
	"google.golang.org/grpc"
)

const (
	registerTimeout   = 5 * time.Second
	agentStartTimeout = 30 * time.Second
	taskTimeout       = 30 * time.Minute
	paneKillTimeout   = 5 * time.Second
	serverStopTimeout = 5 * time.Second
)

// Core manages agentd instances and dispatches tasks.
type Core struct {
	cfg         config.Config
	workspace   string
	logger      *slog.Logger
	logFile     *os.File
	mechaBinary string

	backend  term.Backend
	registry *registry
	spawnMu  sync.Mutex // serializes specialist lookup and respawn

	addr   string
	server *grpc.Server
}

// New creates a new Core.
func New(workspace string, cfg config.Config) (*Core, error) {
	backend, err := term.New()
	if err != nil {
		return nil, fmt.Errorf("core: term backend: %w", err)
	}

	logger, logFile, err := initLogger(workspace)
	if err != nil {
		return nil, err
	}

	return &Core{
		cfg:         cfg,
		workspace:   workspace,
		logger:      logger,
		logFile:     logFile,
		mechaBinary: resolveMechaBinary(),
		backend:     backend,
		registry:    newRegistry(),
	}, nil
}

// resolveMechaBinary returns the mecha binary path. It defaults to the
// current executable so hooks and agentd spawns don't depend on PATH;
// config.MechaBinary (ldflags) wins when overridden.
func resolveMechaBinary() string {
	if config.MechaBinary != "mecha" {
		return config.MechaBinary
	}
	exe, err := os.Executable()
	if err != nil {
		return config.MechaBinary
	}
	return exe
}

// Start starts the gRPC server and runs the coordinator in the foreground,
// blocking until the coordinator exits.
func (c *Core) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("core: listen: %w", err)
	}
	c.addr = ln.Addr().String()

	c.server = grpc.NewServer()
	api.RegisterCoreServer(c.server, &grpcService{core: c})

	go c.server.Serve(ln)
	c.logger.Info("gRPC server listening", "addr", c.addr)

	return c.launchCoordinator(ctx)
}

// shutdown kills all specialist panes, then stops the gRPC server.
func (c *Core) shutdown() {
	for _, inst := range c.registry.all() {
		pane := inst.pane()
		if pane == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), paneKillTimeout)
		_ = c.backend.Kill(ctx, pane)
		cancel()
	}

	stopped := make(chan struct{})
	go func() {
		c.server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(serverStopTimeout):
		c.server.Stop()
		<-stopped
	}
	c.logger.Info("core shutdown complete")

	if c.logFile != nil {
		_ = c.logFile.Close()
	}
}

// coordinatorRole returns the name of the profile's coordinator role.
func (c *Core) coordinatorRole() string {
	for _, r := range c.profileRoles() {
		if r.IsCoordinator {
			return r.Name
		}
	}
	return ""
}

// findRole looks up a role by name.
func (c *Core) findRole(name string) (config.Role, bool) {
	for _, r := range c.profileRoles() {
		if r.Name == name {
			return r, true
		}
	}
	return config.Role{}, false
}

// profileRoles returns all roles of the active profile.
func (c *Core) profileRoles() []config.Role {
	profile, ok := c.cfg.Profiles[c.cfg.Profile]
	if !ok {
		return nil
	}
	return profile.Roles
}
