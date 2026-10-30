package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

func TestWebhookServerHandle(t *testing.T) {
	ch := make(chan agentprovider.HookEvent, 1)
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	defer srv.Close()

	srv.SetParseFunc(func(raw []byte) (agentprovider.HookEvent, error) {
		var ev agentprovider.HookEvent
		return ev, json.Unmarshal(raw, &ev)
	})

	resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json",
		bytes.NewBufferString(`{"event":"SessionStart"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	select {
	case ev := <-ch:
		if ev.Event != agentprovider.EventSessionStart {
			t.Errorf("event = %q, want %q", ev.Event, agentprovider.EventSessionStart)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
}

func TestWebhookServerHandleParseFailure(t *testing.T) {
	ch := make(chan agentprovider.HookEvent, 1)
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	defer srv.Close()

	srv.SetParseFunc(func(raw []byte) (agentprovider.HookEvent, error) {
		return agentprovider.HookEvent{}, fmt.Errorf("unexpected field %q", "hook_event_name")
	})

	resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json",
		bytes.NewBufferString(`{"hook_event_name":"Stop"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	select {
	case ev := <-ch:
		t.Fatalf("event %+v delivered despite parse failure", ev)
	default:
	}
}

func TestTruncateBody(t *testing.T) {
	long := strings.Repeat("x", maxLoggedBody+100)
	got := truncateBody([]byte(long))
	if len(got) != maxLoggedBody+len("...") {
		t.Errorf("truncateBody length = %d, want %d", len(got), maxLoggedBody+len("..."))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncateBody = %q, want suffix ...", got)
	}

	short := "  hello  "
	if got := truncateBody([]byte(short)); got != "hello" {
		t.Errorf("truncateBody(%q) = %q, want %q", short, got, "hello")
	}
}

// A handler blocked on a full event channel must unblock with 503 once the
// server closes, instead of hanging the agent's hook process forever.
func TestWebhookServerCloseUnblocksHandler(t *testing.T) {
	ch := make(chan agentprovider.HookEvent) // unbuffered, no reader: the handler blocks
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	srv.SetParseFunc(func(raw []byte) (agentprovider.HookEvent, error) {
		return agentprovider.HookEvent{Event: agentprovider.EventStop}, nil
	})

	status := make(chan int, 1)
	go func() {
		resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json", bytes.NewReader(nil))
		if err != nil {
			status <- -1
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()

	// Give the handler a moment to block on the channel send.
	time.Sleep(200 * time.Millisecond)
	srv.Close()

	select {
	case code := <-status:
		if code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", code, http.StatusServiceUnavailable)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler still blocked after Close")
	}
}

func TestWebhookServerQueueTimeout(t *testing.T) {
	ch := make(chan agentprovider.HookEvent) // no reader
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	defer srv.Close()
	srv.SetParseFunc(func([]byte) (agentprovider.HookEvent, error) {
		return agentprovider.HookEvent{Event: agentprovider.EventInterrupt}, nil
	})

	start := time.Now()
	resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("queue timeout took %v", elapsed)
	}
}

// TestWebhookServerCloseTimeout bounds Shutdown even when a handler is wedged,
// so agentd can always finish agent cleanup inside Core's pane budget.
func TestWebhookServerCloseTimeout(t *testing.T) {
	ch := make(chan agentprovider.HookEvent, 1)
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		_ = srv.Close()
	}()
	srv.SetParseFunc(func([]byte) (agentprovider.HookEvent, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return agentprovider.HookEvent{Event: agentprovider.EventStop}, nil
	})

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Post("http://"+srv.Addr()+"/webhook", "application/json", bytes.NewReader(nil))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook handler did not start")
	}

	start := time.Now()
	err = srv.Close()
	elapsed := time.Since(start)
	close(release)
	released = true
	<-clientDone

	if err == nil {
		t.Error("Close returned nil despite a wedged handler; want timeout error")
	}
	if elapsed > webhookCloseTimeout+500*time.Millisecond {
		t.Errorf("Close took %v, want <= %v (+500ms tolerance)", elapsed, webhookCloseTimeout)
	}
	t.Logf("webhook Close timed out in %v", elapsed)
}
