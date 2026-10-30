package agentd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

// maxHookBody limits the hook event payload read into memory.
const maxHookBody = 1 << 20 // 1 MiB

const maxLoggedBody = 512

// webhookCloseTimeout bounds Shutdown so a slow/unresponsive hook handler can
// never delay agent process-group cleanup. It is part of agentd's 4s Close
// budget (2s agent grace + 1s kill wait + this 1s webhook wait).
const webhookCloseTimeout = time.Second

// hookEnqueueTimeout keeps hook commands from blocking the agent loop when the
// dispatcher is saturated. It is below Codex's one-second Interrupt budget.
const hookEnqueueTimeout = 250 * time.Millisecond

func truncateBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > maxLoggedBody {
		s = s[:maxLoggedBody] + "..."
	}
	return s
}

// WebhookServer receives and parses agent hook events.
type WebhookServer struct {
	srv       *http.Server
	addr      string
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.RWMutex // guards parseFn
	parseFn func([]byte) (agentprovider.HookEvent, error)
	ch      chan<- agentprovider.HookEvent
}

// NewWebhookServer starts an HTTP server on 127.0.0.1:0.
func NewWebhookServer(ch chan<- agentprovider.HookEvent) (*WebhookServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("webhook: listen: %w", err)
	}

	w := &WebhookServer{
		addr: ln.Addr().String(),
		done: make(chan struct{}),
		ch:   ch,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", w.handle)
	w.srv = &http.Server{Handler: mux}
	go w.srv.Serve(ln)

	return w, nil
}

// Addr returns the listen address (host:port) for the webhook server.
func (w *WebhookServer) Addr() string {
	return w.addr
}

// SetParseFunc sets the hook event parser.
func (w *WebhookServer) SetParseFunc(fn func([]byte) (agentprovider.HookEvent, error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.parseFn = fn
}

// Close shuts down the HTTP server and unblocks in-flight handlers with 503,
// so the agent's hook process never hangs. Shutdown is bounded by
// webhookCloseTimeout; if it expires, active connections are force-closed so a
// wedged handler cannot delay agent shutdown. Safe to call multiple times.
func (w *WebhookServer) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
	})
	ctx, cancel := context.WithTimeout(context.Background(), webhookCloseTimeout)
	defer cancel()
	if err := w.srv.Shutdown(ctx); err != nil {
		_ = w.srv.Close()
		return fmt.Errorf("webhook: shutdown: %w", err)
	}
	return nil
}

func (w *WebhookServer) handle(wr http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(wr, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(wr, r.Body, maxHookBody))
	if err != nil {
		http.Error(wr, "read body", http.StatusBadRequest)
		return
	}

	w.mu.RLock()
	parseFn := w.parseFn
	w.mu.RUnlock()
	if parseFn == nil {
		http.Error(wr, "agent not ready", http.StatusBadRequest)
		return
	}

	ev, err := parseFn(raw)
	if err != nil {
		slog.Warn("webhook: parse hook event", "err", err, "body", truncateBody(raw))
		http.Error(wr, "parse hook event", http.StatusBadRequest)
		return
	}

	// Never block indefinitely: a full channel must not wedge the agent's hook
	// process, especially for ShortTimeout events such as Interrupt.
	timer := time.NewTimer(hookEnqueueTimeout)
	defer timer.Stop()
	select {
	case w.ch <- ev:
		wr.WriteHeader(http.StatusOK)
	case <-w.done:
		http.Error(wr, "server shutting down", http.StatusServiceUnavailable)
	case <-timer.C:
		http.Error(wr, "hook queue is full", http.StatusServiceUnavailable)
	}
}
