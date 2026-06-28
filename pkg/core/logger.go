package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/champly/mecha/pkg/config"
)

func initLogger(workspace string) (*slog.Logger, *os.File, error) {
	dir, err := config.MechaDir()
	if err != nil {
		return nil, nil, err
	}

	name := strings.TrimLeft(workspace, "/")
	name = strings.ReplaceAll(name, "/", "_")
	logDir := filepath.Join(dir, "logs", name)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("core: create log dir %q: %w", logDir, err)
	}

	path := filepath.Join(logDir, time.Now().Format(time.DateOnly)+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("core: open log file %q: %w", path, err)
	}

	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.SourceKey {
				if src, ok := a.Value.Any().(*slog.Source); ok {
					src.File = filepath.Base(src.File)
				}
			}
			return a
		},
	})), f, nil
}
