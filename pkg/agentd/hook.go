package agentd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/champly/mecha/pkg/agent/types"
)

// WebhookServer receives and parses agent hook events.
type WebhookServer struct {
	srv       *http.Server
	addr      string
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.RWMutex // guards parseFn
	parseFn func([]byte) (types.HookEvent, error)
	ch      chan<- types.HookEvent
}

// NewWebhookServer starts an HTTP server on 127.0.0.1:0.
func NewWebhookServer(ch chan<- types.HookEvent) (*WebhookServer, error) {
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
func (w *WebhookServer) SetParseFunc(fn func([]byte) (types.HookEvent, error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.parseFn = fn
}

// Close shuts down the webhook HTTP server and unblocks in-flight handlers
// (they answer 503), so the agent's hook process never hangs. It is safe to
// call multiple times.
func (w *WebhookServer) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.srv.Shutdown(ctx)
}

func (w *WebhookServer) handle(wr http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
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
		http.Error(wr, "parse hook event", http.StatusBadRequest)
		return
	}

	// Never block forever: a full channel during shutdown must not wedge the
	// agent's hook process.
	select {
	case w.ch <- ev:
		wr.WriteHeader(http.StatusOK)
	case <-w.done:
		http.Error(wr, "server shutting down", http.StatusServiceUnavailable)
	}
}
