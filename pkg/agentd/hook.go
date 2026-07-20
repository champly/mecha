package agentd

import (
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/champly/mecha/pkg/agent/types"
)

// WebhookServer receives and parses agent hook events.
type WebhookServer struct {
	srv     *http.Server
	addr    string
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
	w.parseFn = fn
}

// Close shuts down the webhook HTTP server.
func (w *WebhookServer) Close() error {
	return w.srv.Close()
}

func (w *WebhookServer) handle(wr http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(wr, "read body", http.StatusBadRequest)
		return
	}

	if w.parseFn == nil {
		http.Error(wr, "agent not ready", http.StatusBadRequest)
		return
	}

	ev, err := w.parseFn(raw)
	if err != nil {
		http.Error(wr, "parse hook event", http.StatusBadRequest)
		return
	}

	w.ch <- ev
	wr.WriteHeader(http.StatusOK)
}
