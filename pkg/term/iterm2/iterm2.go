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

// activeSession targets iTerm2's currently active session.
const activeSession = "active"

// ITerm2 is a driver.Backend backed by iTerm2 via WebSocket.
type ITerm2 struct {
	mu       sync.Mutex
	conn     *conn
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

func (p *ITerm2) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(); err != nil {
		return nil, err
	}

	var sessionID string
	var err error
	if p.sessions.Empty() {
		// First split: split the current session vertically.
		sessionID, err = p.conn.splitSession(activeSession, true) // vertical
	} else {
		// Subsequent splits: split the last session horizontally.
		sessionID, err = p.conn.splitSession(p.sessions.Last(), false) // horizontal
	}
	if err != nil {
		return nil, err
	}

	if cmd := driver.BuildCommand(spec); cmd != "" {
		// \n alone causes line feed without carriage return.
		// \r\n gives the terminal both: cursor to column 0 + down one line.
		if err := p.conn.sendText(sessionID, cmd+"\r\n"); err != nil {
			return nil, err
		}
	}

	p.sessions.Push(sessionID)
	return driver.NewHandle("iterm2", sessionID), nil
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
