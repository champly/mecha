package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitLogger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logger, f, err := initLogger("/Users/test/project")
	if err != nil {
		t.Fatalf("initLogger() error: %v", err)
	}
	defer f.Close()

	wantPath := filepath.Join(home, ".mecha", "logs", "Users_test_project", time.Now().Format(time.DateOnly)+".log")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("log file %q not created: %v", wantPath, err)
	}

	logger.Info("hello", "key", "value")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), `hello`) || !strings.Contains(string(data), `key=value`) {
		t.Errorf("log file missing expected content, got: %s", data)
	}
}
