package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/champly/mecha/pkg/agent/types"
)

func TestWebhookServerHandle(t *testing.T) {
	ch := make(chan types.HookEvent, 1)
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	defer srv.Close()

	srv.SetParseFunc(func(raw []byte) (types.HookEvent, error) {
		var ev types.HookEvent
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
		if ev.Event != types.EventSessionStart {
			t.Errorf("event = %q, want %q", ev.Event, types.EventSessionStart)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
}

func TestWebhookServerHandleParseFailure(t *testing.T) {
	ch := make(chan types.HookEvent, 1)
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	defer srv.Close()

	srv.SetParseFunc(func(raw []byte) (types.HookEvent, error) {
		return types.HookEvent{}, fmt.Errorf("unexpected field %q", "hook_event_name")
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
	ch := make(chan types.HookEvent) // unbuffered, no reader: the handler blocks
	srv, err := NewWebhookServer(ch)
	if err != nil {
		t.Fatalf("NewWebhookServer: %v", err)
	}
	srv.SetParseFunc(func(raw []byte) (types.HookEvent, error) {
		return types.HookEvent{Event: types.EventStop}, nil
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
