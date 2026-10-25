// Package iterm2 provides a driver.Backend backed by iTerm2 via WebSocket.
package iterm2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
	anchor   string
	sessions driver.Chain
	out      io.Writer
}

// New creates a new ITerm2 provider. The WebSocket connection is established
// lazily on the first Spawn, so an unavailable iTerm2 API does not prevent
// mecha from starting. The anchor session is pinned to this process's session
// (ITERM_SESSION_ID) so spawned panes stay in the coordinator's tab; Spawn
// falls back to the active session when it is unavailable.
func New() (driver.Backend, error) {
	return &ITerm2{anchor: ownSession(), out: os.Stdout}, nil
}

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

func badgePayload(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

func badgeCommand(text string) string {
	return fmt.Sprintf("printf '\\033]1337;SetBadgeFormat=%s\\007'", badgePayload(text))
}

// Label implements driver.Backend; iTerm2 shows the text as the pane badge.
func (p *ITerm2) Label(text string) error {
	out := p.out
	if out == nil {
		out = os.Stdout
	}
	_, err := fmt.Fprintf(out, "\x1b]1337;SetBadgeFormat=%s\x07", badgePayload(text))
	return err
}

func (p *ITerm2) Spawn(ctx context.Context, spec driver.Spec) (driver.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureConn(ctx); err != nil {
		return nil, err
	}

	var sessionID string
	var err error
	if p.sessions.Empty() {
		// First split: split the anchor session vertically.
		target := p.anchor
		if target == "" {
			target = activeSession
		}
		sessionID, err = p.conn.splitSession(ctx, target, true) // vertical
	} else {
		// Subsequent splits: split the last session horizontally.
		sessionID, err = p.conn.splitSession(ctx, p.sessions.Last(), false) // horizontal
	}
	if err != nil {
		return nil, err
	}

	if cmd := driver.BuildCommand(spec); cmd != "" {
		if spec.Role != "" {
			cmd = badgeCommand(spec.Role) + " && " + cmd
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
