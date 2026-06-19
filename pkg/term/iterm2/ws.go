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

	"github.com/champly/mecha/pkg/term/iterm2/api"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func socketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/Application Support/iTerm2/private/socket")
}

func requestCookie() (cookie, key string, err error) {
	out, err := exec.Command("osascript", "-e",
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
	ws  *websocket.Conn
	seq int64
}

func dial() (*conn, error) {
	cookie, key, err := requestCookie()
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

	ws, _, err := dialer.DialContext(context.Background(), "ws://localhost", headers)
	if err != nil {
		return nil, fmt.Errorf("iterm2: dial: %w", err)
	}
	return &conn{ws: ws}, nil
}

func (c *conn) close() error {
	return c.ws.Close()
}

func (c *conn) call(req *api.ClientOriginatedMessage) (*api.ServerOriginatedMessage, error) {
	c.seq++
	req.Id = proto.Int64(c.seq)

	body, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("iterm2: marshal: %w", err)
	}

	// Write with timeout.
	c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.ws.WriteMessage(websocket.BinaryMessage, body); err != nil {
		return nil, fmt.Errorf("iterm2: write: %w", err)
	}

	// Read with timeout, skipping notifications.
	c.ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("iterm2: read: %w", err)
		}
		resp := &api.ServerOriginatedMessage{}
		if err := proto.Unmarshal(data, resp); err != nil {
			return nil, fmt.Errorf("iterm2: unmarshal: %w", err)
		}
		if resp.GetId() == 0 {
			// notification, skip
			c.ws.SetReadDeadline(time.Now().Add(30 * time.Second))
			continue
		}
		if resp.GetId() != req.GetId() {
			return nil, fmt.Errorf("iterm2: unexpected response id: %d (want %d)", resp.GetId(), req.GetId())
		}
		return resp, nil
	}
}

func (c *conn) splitSession(sessionID string, vertical bool) (string, error) {
	dir := api.SplitPaneRequest_VERTICAL
	if !vertical {
		dir = api.SplitPaneRequest_HORIZONTAL
	}
	resp, err := c.call(&api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SplitPaneRequest{
			SplitPaneRequest: &api.SplitPaneRequest{
				Session:        proto.String(sessionID),
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

func (c *conn) sendText(sessionID, text string) error {
	_, err := c.call(&api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SendTextRequest{
			SendTextRequest: &api.SendTextRequest{
				Session: proto.String(sessionID),
				Text:    proto.String(text),
			},
		},
	})
	return err
}

func (c *conn) getBuffer(sessionID string, all bool) (string, error) {
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_GetBufferRequest{
			GetBufferRequest: &api.GetBufferRequest{
				Session: proto.String(sessionID),
			},
		},
	}
	if all {
		// Request entire scrollback buffer.
		n := int32(-1)
		req.GetGetBufferRequest().LineRange = &api.LineRange{TrailingLines: &n}
	}
	resp, err := c.call(req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, line := range resp.GetGetBufferResponse().GetContents() {
		b.WriteString(line.GetText())
	}
	return b.String(), nil
}

func (c *conn) closeSessions(sessionIDs ...string) error {
	_, err := c.call(&api.ClientOriginatedMessage{
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
