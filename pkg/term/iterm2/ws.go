package iterm2

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/champly/mecha/pkg/term/iterm2/api"
)

func socketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/Application Support/iTerm2/private/socket")
}

func requestCookie(ctx context.Context) (cookie, key string, err error) {
	// Bound osascript: a hung iTerm2 must not wedge startup.
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(actx, "osascript", "-e",
		`tell application "iTerm2" to request cookie and key for app named "mecha"`,
	).Output()
	if err != nil {
		return "", "", fmt.Errorf("iterm2: osascript: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return "", "", fmt.Errorf("iterm2: unexpected cookie response: %q", string(out))
	}
	return parts[0], parts[1], nil
}

type conn struct {
	ws   *websocket.Conn
	seq  int64
	dead bool // guarded by ITerm2.mu; the connection failed and must be redialed on next use
}

func dial(ctx context.Context) (*conn, error) {
	cookie, key, err := requestCookie(ctx)
	if err != nil {
		return nil, err
	}

	dialer := websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath())
		},
		Subprotocols: []string{"api.iterm2.com"},
	}
	headers := http.Header{}
	headers.Set("origin", "ws://localhost/")
	headers.Set("x-iterm2-library-version", "go mecha")
	headers.Set("x-iterm2-disable-auth-ui", "true")
	headers.Set("x-iterm2-cookie", cookie)
	headers.Set("x-iterm2-key", key)

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, _, err := dialer.DialContext(dctx, "ws://localhost", headers)
	if err != nil {
		return nil, fmt.Errorf("iterm2: dial: %w", err)
	}
	return &conn{ws: ws}, nil
}

// fail marks the connection dead so the next Spawn redials.
func (c *conn) fail(err error) error {
	_ = c.ws.Close()
	c.dead = true
	return err
}

func (c *conn) close() error {
	return c.ws.Close()
}

// deadline returns the earlier of the timeout and the context deadline.
func deadline(ctx context.Context, timeout time.Duration) time.Time {
	if d, ok := ctx.Deadline(); ok && time.Until(d) < timeout {
		return d
	}
	return time.Now().Add(timeout)
}

func (c *conn) call(ctx context.Context, req *api.ClientOriginatedMessage) (*api.ServerOriginatedMessage, error) {
	c.seq++
	req.Id = &c.seq

	body, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("iterm2: marshal: %w", err)
	}

	// Write with deadline.
	c.ws.SetWriteDeadline(deadline(ctx, 10*time.Second))
	if err := c.ws.WriteMessage(websocket.BinaryMessage, body); err != nil {
		return nil, c.fail(fmt.Errorf("iterm2: write: %w", err))
	}

	// Read with deadline, skipping notifications.
	c.ws.SetReadDeadline(deadline(ctx, 30*time.Second))
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, c.fail(fmt.Errorf("iterm2: read: %w", err))
		}
		resp := &api.ServerOriginatedMessage{}
		if err := proto.Unmarshal(data, resp); err != nil {
			// A malformed frame is a protocol desync; drop the connection.
			return nil, c.fail(fmt.Errorf("iterm2: unmarshal: %w", err))
		}
		if resp.GetId() == 0 {
			// notification, skip
			c.ws.SetReadDeadline(deadline(ctx, 30*time.Second))
			continue
		}
		if resp.GetId() != req.GetId() {
			return nil, c.fail(fmt.Errorf("iterm2: unexpected response id: %d (want %d)", resp.GetId(), req.GetId()))
		}
		return resp, nil
	}
}

func (c *conn) splitSession(ctx context.Context, sessionID string, vertical bool) (string, error) {
	dir := api.SplitPaneRequest_VERTICAL
	if !vertical {
		dir = api.SplitPaneRequest_HORIZONTAL
	}
	resp, err := c.call(ctx, &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SplitPaneRequest{
			SplitPaneRequest: &api.SplitPaneRequest{
				Session:        &sessionID,
				SplitDirection: &dir,
			},
		},
	})
	if err != nil {
		return "", err
	}
	sids := resp.GetSplitPaneResponse().GetSessionId()
	if len(sids) == 0 {
		return "", fmt.Errorf("iterm2: split: no session_id")
	}
	return sids[0], nil
}

func (c *conn) sendText(ctx context.Context, sessionID, text string) error {
	_, err := c.call(ctx, &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SendTextRequest{
			SendTextRequest: &api.SendTextRequest{
				Session: &sessionID,
				Text:    &text,
			},
		},
	})
	return err
}

func (c *conn) closeSessions(ctx context.Context, sessionIDs ...string) error {
	_, err := c.call(ctx, &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_CloseRequest{
			CloseRequest: &api.CloseRequest{
				Target: &api.CloseRequest_Sessions{
					Sessions: &api.CloseRequest_CloseSessions{
						SessionIds: sessionIDs,
					},
				},
			},
		},
	})
	return err
}
