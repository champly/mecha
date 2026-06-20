// Package iterm2 provides a driver.Backend backed by iTerm2 via WebSocket.
package iterm2

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/champly/mecha/pkg/term/driver"
)

// ITerm2 is a driver.Provider backed by iTerm2 via WebSocket.
type ITerm2 struct {
	mu       sync.Mutex
	conn     *conn
	windowID string
	sessions driver.Chain
}

// New creates a new ITerm2 provider. It dials the iTerm2 WebSocket
// immediately rather than lazily on first use.
func New() (driver.Backend, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	return &ITerm2{conn: c}, nil
}

// Name returns the driver name.
func (p *ITerm2) Name() string {
	return "iterm2"
}

// Match reports whether the current environment is iTerm2.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "iterm")
}

func (p *ITerm2) ensureConn() error {
	if p.conn != nil {
		return nil
	}
	c, err := dial()
	if err != nil {
		return err
	}
	p.conn = c
	return nil
}

func (p *ITerm2) currentSession() (string, error) {
	// iTerm2 doesn't expose "current session" directly via the raw API.
	// We use "active" as a special session identifier accepted by iTerm2.
	return "active", nil
}

func (p *ITerm2) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(); err != nil {
		return nil, err
	}

	cmd := driver.BuildBootstrap(spec)

	var sessionID string
	var err error
	if p.sessions.Empty() {
		// First split: split the current session vertically.
		cur, err := p.currentSession()
		if err != nil {
			return nil, err
		}
		sessionID, err = p.conn.splitSession(cur, true) // vertical
		if err != nil {
			return nil, err
		}
	} else {
		// Subsequent splits: split the last session horizontally.
		sessionID, err = p.conn.splitSession(p.sessions.Last(), false) // horizontal
		if err != nil {
			return nil, err
		}
	}

	if cmd != "" {
		// \n alone causes line feed without carriage return.
		// \r\n gives the terminal both: cursor to column 0 + down one line.
		if err := p.conn.sendText(sessionID, cmd+"\r\n"); err != nil {
			return nil, err
		}
	}

	p.sessions.Push(sessionID)
	return driver.NewID("iterm2", sessionID), nil
}

func (p *ITerm2) Send(ctx context.Context, h driver.Handle, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(); err != nil {
		return err
	}

	// \n alone causes line feed without carriage return.
	// Replace with \r\n so the terminal gets both cursor reset and line feed.
	text = strings.ReplaceAll(text, "\n", "\r\n")
	return p.conn.sendText(h.PaneID(), text)
}

func (p *ITerm2) Capture(ctx context.Context, h driver.Handle) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(); err != nil {
		return "", err
	}
	return p.conn.getBuffer(h.PaneID(), false)
}

func (p *ITerm2) CaptureAll(ctx context.Context, h driver.Handle) (string, error) {
	return p.conn.getBuffer(h.PaneID(), true)
}

func (p *ITerm2) Kill(ctx context.Context, h driver.Handle) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil {
		return nil
	}

	paneID := h.PaneID()
	if err := p.conn.closeSessions(paneID); err != nil {
		return fmt.Errorf("iterm2: close session: %w", err)
	}

	p.sessions.Remove(paneID)
	if p.sessions.Empty() {
		p.conn.close()
		p.conn = nil
	}
	return nil
}
