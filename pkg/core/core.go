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
	paneKillTimeout   = 5 * time.Second
	// paneShutdownTimeout bounds terminal cleanup after the coordinator exits.
	// User-facing shutdown must not wait paneKillTimeout once per specialist.
	paneShutdownTimeout = time.Second
)

// Core manages agentd instances and dispatches tasks.
type Core struct {
	cfg         config.Config
	workspace   string
	logger      *slog.Logger
	logFile     *os.File
	mechaBinary string

	backend    term.Backend
	registry   *registry
	spawnMu    sync.Mutex // guards spawnLocks
	spawnLocks map[string]*sync.Mutex

	addr   string
	server *grpc.Server
}

// New creates a new Core.
func New(workspace string, cfg config.Config) (*Core, error) {
	backend, err := term.New()
	if err != nil {
		return nil, fmt.Errorf("core: term backend: %w", err)
	}

	logger, logFile, err := config.NewFileLogger(workspace)
	if err != nil {
		_ = backend.Close()
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
		spawnLocks:  make(map[string]*sync.Mutex),
	}, nil
}

// Logger returns the core's logger.
func (c *Core) Logger() *slog.Logger {
	return c.logger
}

// resolveMechaBinary returns the mecha binary path: config.MechaBinary (set
// via ldflags) wins, otherwise the current executable is used so spawned
// hooks do not depend on PATH.
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

// Start starts the gRPC server and runs the coordinator in the foreground
// until it exits.
func (c *Core) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("core: listen: %w", err)
	}
	c.addr = ln.Addr().String()

	c.server = grpc.NewServer()
	api.RegisterCoreServer(c.server, &grpcService{core: c})

	go func() {
		// Serve returns only on a listener failure or after Stop/GracefulStop.
		if err := c.server.Serve(ln); err != nil {
			c.logger.Error("gRPC server failed", "err", err)
		}
	}()
	c.logger.Info("gRPC server listening", "addr", c.addr)

	err = c.launchCoordinator(ctx)
	c.shutdown()
	return err
}

// shutdown stops the gRPC server, then cleans up specialist panes. Stopping
// RPCs first aborts outstanding asks and makes every agentd exit promptly;
// pane cleanup is parallel and bounded so lingering AppleScript operations
// cannot keep the interactive process alive.
func (c *Core) shutdown() {
	c.server.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), paneShutdownTimeout)
	c.killAllPanes(ctx)
	cancel()

	c.logger.Info("core shutdown complete")

	_ = c.backend.Close()

	if c.logFile != nil {
		_ = c.logFile.Close()
	}
}

// killAllPanes kills every specialist pane concurrently. The shared context
// bounds total cleanup time; each backend still has its own upper bound.
func (c *Core) killAllPanes(ctx context.Context) {
	instances := c.registry.all()
	var wg sync.WaitGroup
	for _, inst := range instances {
		pane := inst.pane()
		if pane == nil {
			continue
		}
		wg.Add(1)
		go func(inst *instance, pane term.Handle) {
			defer wg.Done()
			killCtx, cancel := context.WithTimeout(ctx, paneKillTimeout)
			defer cancel()
			if err := c.backend.Kill(killCtx, pane); err != nil && ctx.Err() == nil {
				c.logger.Warn("kill pane failed", "role", inst.role, "id", inst.id, "err", err)
			}
		}(inst, pane)
	}
	wg.Wait()
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
