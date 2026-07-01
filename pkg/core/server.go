package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/champly/mecha/pkg/agent/types"
)

const maxRequestBody = 1 << 20 // 1 MiB

func (c *Core) startHTTPServer(ln net.Listener) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", c.handleWebhook)
	mux.HandleFunc("/ask", c.handleAsk)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv
}

func (c *Core) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := strings.TrimPrefix(r.URL.Path, "/webhook/")
	if agentID == "" {
		http.Error(w, "missing agent ID", http.StatusBadRequest)
		return
	}

	c.mu.Lock()
	a, ok := c.agentByID[agentID]
	c.mu.Unlock()
	if !ok {
		http.Error(w, "unknown agent: "+agentID, http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	event, err := a.ParseHookEvent(body)
	if err != nil {
		c.logger.Error("webhook parse error", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	c.onEvent(agentID, event)
	w.WriteHeader(http.StatusOK)
}

func (c *Core) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Role string `json:"role"`
		Task string `json:"task"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := c.Ask(r.Context(), req.Role, req.Task)
	if err != nil {
		c.logger.Error("ask failed", "role", req.Role, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if result.err != "" {
		c.logger.Error("task failed", "role", req.Role, "err", result.err)
		http.Error(w, result.err, http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, result.output)
}

func (c *Core) onEvent(agentID string, event types.HookEvent) {
	c.mu.Lock()
	inst := c.instanceByAgentID[agentID]
	c.mu.Unlock()
	if inst == nil {
		return
	}

	c.logger.Info(
		"hook event",
		"event", event.Event,
		"role", inst.role,
		"agent", agentID,
		"session", event.SessionID,
	)

	switch event.Event {
	case types.EventSessionStart:
		if inst.status.Load() == statusStarting {
			close(inst.ready)
		}

	case types.EventStop:
		if inst.status.Load() == statusBusy {
			if p := inst.result.Load(); p != nil {
				select {
				case *p <- taskResult{output: event.Output}:
				default:
				}
			}
		}

	case types.EventStopFailure:
		if inst.status.Load() == statusBusy {
			if p := inst.result.Load(); p != nil {
				select {
				case *p <- taskResult{err: event.Error}:
				default:
				}
			}
		}
	}
}
