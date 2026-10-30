// Package iterm2 provides a driver.Backend backed by iTerm2 via WebSocket.
package iterm2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/champly/mecha/pkg/term/driver"
)

// ITerm2 is a driver.Backend backed by iTerm2 via WebSocket.
type ITerm2 struct {
	mu       sync.Mutex
	conn     *conn
	anchor   string
	sessions driver.Chain
}

// New creates a new ITerm2 provider. The WebSocket connection is established
// lazily on the first Spawn, so an unavailable iTerm2 API does not prevent
// mecha from starting. The anchor session is pinned to this process's session
// (ITERM_SESSION_ID); New fails when it is unavailable so panes can never
// land in another session.
func New() (driver.Backend, error) {
	anchor := ownSession()
	if anchor == "" {
		err := errors.New("iterm2: ITERM_SESSION_ID is empty")
		return nil, fmt.Errorf("%w: %w", driver.ErrAnchorUnavailable, err)
	}
	return &ITerm2{anchor: anchor}, nil
}

// screenWait bounds how long a fresh pane may take to print its first prompt
// before agentd's command is typed into the shell.
const screenWait = 10 * time.Second

// ownSession returns the id of the iTerm2 session this process runs in,
// parsed from ITERM_SESSION_ID ("w0t0p0:GUID"). Empty when unavailable.
func ownSession() string {
	if _, guid, ok := strings.CutLast(os.Getenv("ITERM_SESSION_ID"), ":"); ok {
		return guid
	}
	return ""
}

// Match reports whether the current environment is iTerm2.
func Match() bool {
	return strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "iterm")
}

func (p *ITerm2) ensureConn(ctx context.Context) error {
	if p.conn != nil && !p.conn.dead {
		return nil
	}
	// Redial dead connections.
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	p.conn = c
	return nil
}

func (p *ITerm2) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.anchor == "" {
		return nil, errors.New("iterm2: empty anchor session")
	}
	if err := p.ensureConn(ctx); err != nil {
		return nil, err
	}

	var sessionID string
	var err error
	if p.sessions.Empty() {
		// First split: split the anchor session vertically.
		sessionID, err = p.conn.splitSession(ctx, p.anchor, true) // vertical
	} else {
		// Subsequent splits: split the last session horizontally.
		sessionID, err = p.conn.splitSession(ctx, p.sessions.Last(), false) // horizontal
	}
	if err != nil {
		return nil, err
	}

	if cmd := driver.BuildCommand(spec); cmd != "" {
		if err := p.conn.awaitScreen(ctx, sessionID, screenWait); err != nil {
			if cerr := p.closeOrphan(ctx, sessionID); cerr != nil {
				p.sessions.Push(sessionID)
			}
			return nil, err
		}
		// \r\n returns the cursor to column 0 and moves down a line; \n alone
		// does not.
		if err := p.conn.sendText(ctx, sessionID, cmd+"\r\n"); err != nil {
			// The split already happened; close the session, tracking it only
			// when that fails.
			if cerr := p.closeOrphan(ctx, sessionID); cerr != nil {
				p.sessions.Push(sessionID)
			}
			return nil, err
		}
	}

	p.sessions.Push(sessionID)
	return driver.NewHandle("iterm2", sessionID), nil
}

// closeOrphan closes a session Spawn created but could not configure,
// redialing first when the send killed the connection.
func (p *ITerm2) closeOrphan(ctx context.Context, sessionID string) error {
	if p.conn.dead {
		c, err := dial(ctx)
		if err != nil {
			return err
		}
		p.conn = c
	}
	return p.conn.closeSessions(ctx, sessionID)
}

func (p *ITerm2) Kill(ctx context.Context, h driver.Handle) error {
	if h == nil || h.PaneID() == "" {
		return errors.New("iterm2: empty pane id")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil {
		return nil
	}

	paneID := h.PaneID()
	err := p.conn.closeSessions(ctx, paneID)
	// Drop the id even on failure, so a stale session cannot break later splits.
	p.sessions.Remove(paneID)
	if p.sessions.Empty() {
		p.conn.close()
		p.conn = nil
	}
	if err != nil {
		return fmt.Errorf("iterm2: close session: %w", err)
	}
	return nil
}

// Close closes the WebSocket connection to iTerm2, if any.
func (p *ITerm2) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	err := p.conn.close()
	p.conn = nil
	return err
}
