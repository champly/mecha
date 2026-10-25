package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/champly/mecha/pkg/agent"
	"github.com/champly/mecha/pkg/config"
	"github.com/champly/mecha/pkg/core"
	"github.com/champly/mecha/pkg/term"
	"github.com/spf13/cobra"
)

// minServeShutdownTimeout leaves agentd enough room for its 4s Close budget
// (2s SIGTERM grace + 1s post-SIGKILL wait + 1s webhook shutdown) plus a 1s
// reserve before Core's own pane kill deadline.
const minServeShutdownTimeout = 5 * time.Second

func newServeCmd() *cobra.Command {
	var (
		workspace       string
		addr            string
		logLevel        string
		shutdownTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run Core as a headless daemon serving specialist agents",
		Long: "Run Core without the interactive coordinator. Specialists are launched " +
			"on private PTYs and can be driven with `mecha ask`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := resolveServeWorkspace(workspace)
			if err != nil {
				return err
			}

			level, err := parseLogLevel(logLevel)
			if err != nil {
				return err
			}
			if shutdownTimeout < minServeShutdownTimeout {
				return fmt.Errorf("serve: --shutdown-timeout must be at least %s (agentd needs 2s TERM grace + 1s kill wait + 1s webhook close + 1s reserve)", minServeShutdownTimeout)
			}
			if err := core.ValidateLoopbackAddr(addr); err != nil {
				return fmt.Errorf("serve: %w", err)
			}

			cfg, err := config.LoadConfig(configPath, agent.ValidateAgentType)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			backend, err := term.New(term.Options{
				Kind:   "headless",
				Stdout: os.Stdout,
				Stderr: os.Stderr,
			})
			if err != nil {
				return fmt.Errorf("serve: headless backend: %w", err)
			}
			// Core owns and closes the backend on every Run path. This defer is
			// a defensive fallback for early returns; Close is idempotent.
			defer func() { _ = backend.Close() }()

			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
			c, err := core.NewWithOptions(core.Options{
				Workspace:       ws,
				Config:          cfg,
				Backend:         backend,
				Addr:            addr,
				Logger:          logger,
				ShutdownTimeout: shutdownTimeout,
				Serve:           true,
				// Print the actual address unconditionally: with a dynamic port
				// this is the only way to discover it, even at --log-level error.
				OnListening: func(addr string) {
					fmt.Fprintf(os.Stdout, "mecha serve listening on %s\n", addr)
				},
			})
			if err != nil {
				return fmt.Errorf("serve: create core: %w", err)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			slog.SetDefault(logger)
			slog.Info("mecha serve starting",
				"workspace", ws,
				"requested_addr", addr,
				"shutdown_timeout", shutdownTimeout,
			)

			if err := c.Run(ctx); err != nil {
				return err
			}
			slog.Info("mecha serve stopped")
			return nil
		},
	}

	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace directory (default: current directory)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "gRPC listen address (host:port)")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	cmd.Flags().DurationVar(&shutdownTimeout, "shutdown-timeout", 10*time.Second, "Total pane cleanup budget during shutdown (minimum 5s)")

	return cmd
}

// resolveServeWorkspace resolves and validates the workspace directory.
func resolveServeWorkspace(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("serve: get workspace: %w", err)
		}
		path = wd
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("serve: resolve workspace %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("serve: workspace %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("serve: workspace %q is not a directory", path)
	}
	return abs, nil
}

// parseLogLevel maps the flag value onto a slog level.
func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("serve: invalid --log-level %q (want debug, info, warn, or error)", value)
	}
}
