// Package core runs the orchestration gRPC service and manages agentd instances.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
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
	// paneKillTimeout bounds cleanup of one unhealthy pane during a respawn.
	// It is not the global shutdown budget.
	paneKillTimeout = 5 * time.Second

	defaultShutdownTimeout = 10 * time.Second
	// minPaneShutdownBudget covers agentd's 4s Close budget: 2s SIGTERM
	// grace + 1s post-SIGKILL wait + 1s webhook Shutdown, plus a 1s reserve.
	minPaneShutdownBudget = 5 * time.Second

	defaultAddr = "127.0.0.1:0"
)

// Options configures a Core.
type Options struct {
	// Workspace is the project directory shared with agentd processes.
	Workspace string
	// Config is the loaded mecha configuration.
	Config config.Config
	// Backend is the terminal backend. Nil selects term.New().
	Backend term.Backend
	// Addr is the gRPC listen address. Empty selects 127.0.0.1:0.
	Addr string
	// Logger receives process logs. Nil selects a stdout text handler.
	Logger *slog.Logger
	// ShutdownTimeout is the total budget shared by all specialist pane
	// cleanups. Zero uses 10s; values below 5s are raised to the minimum pane
	// budget (agentd needs 2s TERM grace + 1s kill wait + 1s webhook close,
	// leaving a 1s reserve).
	ShutdownTimeout time.Duration
	// Serve disables the interactive coordinator and only serves specialist
	// Ask RPCs. Run blocks until the context is cancelled.
	Serve bool
	// OnListening is called with the actual address after net.Listen succeeds.
	// It is safe to use for unconditional user-facing output.
	OnListening func(addr string)
}

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

	listenAddr      string
	serve           bool
	shutdownTimeout time.Duration
	onListening     func(addr string)

	addrMu   sync.RWMutex
	addr     string
	server   *grpc.Server
	serveErr chan error

	shutdownOnce sync.Once
}

// New creates a new Core with the historical run-mode defaults. It is kept for
// compatibility; callers that need serve mode or dependency injection should
// use NewWithOptions.
func New(workspace string, cfg config.Config) (*Core, error) {
	logger, logFile, err := config.NewFileLogger(workspace)
	if err != nil {
		return nil, err
	}

	c, err := NewWithOptions(Options{Workspace: workspace, Config: cfg, Logger: logger})
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	c.logFile = logFile
	return c, nil
}

// NewWithOptions creates a Core from explicit options.
func NewWithOptions(opts Options) (*Core, error) {
	backend := opts.Backend
	if backend == nil {
		var err error
		backend, err = term.New()
		if err != nil {
			return nil, fmt.Errorf("core: term backend: %w", err)
		}
	}

	logger := opts.Logger
	if logger == nil {
		logger = defaultLogger()
	}

	addr := opts.Addr
	if addr == "" {
		addr = defaultAddr
	}

	timeout := opts.ShutdownTimeout
	switch {
	case timeout <= 0:
		timeout = defaultShutdownTimeout
	case timeout < minPaneShutdownBudget:
		timeout = minPaneShutdownBudget
	}

	return &Core{
		cfg:             opts.Config,
		workspace:       opts.Workspace,
		logger:          logger,
		mechaBinary:     resolveMechaBinary(),
		backend:         backend,
		registry:        newRegistry(),
		spawnLocks:      make(map[string]*sync.Mutex),
		listenAddr:      addr,
		serve:           opts.Serve,
		shutdownTimeout: timeout,
		onListening:     opts.OnListening,
	}, nil
}

// defaultLogger returns a process logger that writes to stdout. Daemon logs
// must be visible in the terminal of the supervising process.
func defaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Logger returns the core's logger.
func (c *Core) Logger() *slog.Logger {
	return c.logger
}

// log returns a logger that is safe to use on a partially constructed Core.
func (c *Core) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Addr returns the actual gRPC listen address, or "" before the server starts.
func (c *Core) Addr() string {
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	return c.addr
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
	return c.run(ctx, false)
}

// Run starts the gRPC server and blocks until the context is cancelled. In
// serve mode it does not launch the coordinator, so only specialist Ask RPCs
// are served. Without Serve it is equivalent to Start.
func (c *Core) Run(ctx context.Context) error {
	return c.run(ctx, c.serve)
}

func (c *Core) run(ctx context.Context, serve bool) error {
	if err := c.listen(); err != nil {
		// The injected backend is owned by Core; close it even when the
		// listener never came up.
		c.shutdown()
		return err
	}
	defer c.shutdown()

	if serve {
		select {
		case <-ctx.Done():
			return nil
		case err := <-c.serveErr:
			return err
		}
	}

	// Run mode: keep the coordinator in the foreground, but abort it if the
	// gRPC server dies unexpectedly instead of blocking until a signal.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var serveErr error
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case err := <-c.serveErr:
			if err != nil {
				serveErr = err
				cancel()
			}
		case <-runCtx.Done():
		}
	}()

	coordErr := c.launchCoordinator(runCtx)
	cancel()
	<-watcherDone
	if serveErr != nil {
		return serveErr
	}
	return coordErr
}

// listen validates the address, binds it, and starts serving gRPC in the
// background. The actual address is reported through Addr and OnListening.
func (c *Core) listen() error {
	addr := c.listenAddr
	if addr == "" {
		addr = defaultAddr
	}
	if err := ValidateLoopbackAddr(addr); err != nil {
		return fmt.Errorf("core: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("core: listen: %w", err)
	}

	c.addrMu.Lock()
	c.addr = ln.Addr().String()
	c.addrMu.Unlock()

	c.server = grpc.NewServer()
	api.RegisterCoreServer(c.server, &grpcService{core: c})

	serveErr := make(chan error, 1)
	c.serveErr = serveErr
	go func() {
		serveErr <- normalizeServeError(c.server.Serve(ln))
	}()

	c.notifyListening(c.Addr())
	c.log().Info("gRPC server listening", "addr", c.Addr())
	return nil
}

// notifyListening invokes the optional callback without allowing a user
// callback panic to escape Run or leave the listener/backend unclosed.
func (c *Core) notifyListening(addr string) {
	if c.onListening == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			c.log().Error("onListening callback panic", "addr", addr, "panic", r)
		}
	}()
	c.onListening(addr)
}

// normalizeServeError drops the expected error from grpc.Server.Stop and
// preserves unexpected Serve failures for Run to return.
func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return fmt.Errorf("core: gRPC serve: %w", err)
}

// shutdown stops the gRPC server, then cleans up specialist panes. Stopping
// RPCs first aborts outstanding asks and makes every agentd exit promptly.
// All pane kills share one ShutdownTimeout budget and run in parallel. It is
// idempotent and tolerates a partially constructed Core.
func (c *Core) shutdown() {
	c.shutdownOnce.Do(func() {
		if c.server != nil {
			c.server.Stop()
		}

		timeout := c.shutdownTimeout
		switch {
		case timeout <= 0:
			timeout = defaultShutdownTimeout
		case timeout < minPaneShutdownBudget:
			timeout = minPaneShutdownBudget
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		c.killAllPanes(ctx)
		cancel()

		if c.backend != nil {
			if err := c.backend.Close(); err != nil {
				c.log().Error("close backend failed", "err", err)
			}
		}

		// Log completion only after every resource has been released.
		c.log().Info("core shutdown complete")

		if c.logFile != nil {
			_ = c.logFile.Close()
		}
	})
}

// killAllPanes kills every specialist pane concurrently. Every pane shares the
// same deadline for graceful termination. Escalation/reaping after that
// deadline is bounded by the backend implementation (headless uses a bounded
// SIGKILL wait); backend.Close remains the final, bounded escalation step.
func (c *Core) killAllPanes(ctx context.Context) {
	if c.backend == nil || c.registry == nil {
		return
	}
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
			if err := c.backend.Kill(ctx, pane); err != nil && ctx.Err() == nil {
				c.log().Warn("kill pane failed", "role", inst.role, "id", inst.id, "err", err)
			}
		}(inst, pane)
	}
	wg.Wait()
}

// ValidateLoopbackAddr rejects addresses that could expose the unauthenticated
// gRPC API beyond the local machine. Only 127.0.0.0/8, ::1, and localhost
// (when every resolved address is loopback) are accepted. The port must be a
// decimal number in [0, 65535].
func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("listen address %q must specify a loopback host (127.0.0.1, ::1, or localhost)", addr)
	}
	if port == "" {
		return fmt.Errorf("listen address %q must specify a port", addr)
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return fmt.Errorf("listen port %q must be a decimal number in 0-65535", port)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("listen port %q must be a decimal number in 0-65535", port)
	}
	if strings.EqualFold(host, "localhost") {
		ips, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("resolve localhost: %w", err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("localhost did not resolve to any address")
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return fmt.Errorf("localhost resolved to non-loopback address %s", ip)
			}
		}
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen host %q is not an IP address; use 127.0.0.1, ::1, or localhost", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("listen host %q is not loopback; mecha only listens on 127.0.0.0/8, ::1, or localhost", host)
	}
	return nil
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
