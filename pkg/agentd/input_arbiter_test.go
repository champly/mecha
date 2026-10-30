package agentd

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

func TestInputArbiterSerializesTaskAndUserInput(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	stop := make(chan struct{})
	events := make(chan inputEvent, 16)
	a := newInputArbiter(func() *os.File { return pw }, events, stop)
	go a.run()
	t.Cleanup(func() { close(stop) })

	profile := agentprovider.TUIProfile{PasteMode: agentprovider.PasteBracketed, SubmitKey: []byte("\r")}
	if !a.taskText(profile, "task") {
		t.Fatal("taskText was rejected")
	}
	if !a.user([]byte("U")) {
		t.Fatal("user input was rejected")
	}
	if !a.taskEnter(profile) {
		t.Fatal("taskEnter was rejected")
	}
	if !a.endTask(false) {
		t.Fatal("endTask was rejected")
	}

	waitInputAction(t, events, inputTaskText)
	waitInputAction(t, events, inputTaskEnter)
	select {
	case ev := <-events:
		if ev.action != inputEndTask || ev.err != nil {
			t.Fatalf("end event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task end")
	}

	_ = pw.Close()
	data, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("read PTY data: %v", err)
	}
	want := bracketedPasteStart + "task" + bracketedPasteEnd + "\rU"
	if got := string(data); got != want {
		t.Fatalf("PTY bytes = %q, want %q", got, want)
	}
}

func TestInputArbiterBuffersUserInputWithBound(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	stop := make(chan struct{})
	events := make(chan inputEvent, 16)
	a := newInputArbiter(func() *os.File { return pw }, events, stop)
	go a.run()
	t.Cleanup(func() { close(stop) })

	a.transaction = true
	a.handleUser(make([]byte, maxBufferedUserInput+1))
	select {
	case ev := <-events:
		if !ev.overflow || ev.err == nil {
			t.Fatalf("overflow event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for overflow event")
	}
}

func TestEncodeTaskInputUsesProviderCapabilities(t *testing.T) {
	got := string(encodeTaskInput(agentprovider.TUIProfile{PasteMode: agentprovider.PasteBracketed}, "a\nb"+bracketedPasteEnd+"c"))
	if !strings.HasPrefix(got, bracketedPasteStart) {
		t.Fatalf("bracketed payload missing start marker: %q", got)
	}
	if strings.Count(got, bracketedPasteEnd) != 1 {
		t.Fatalf("payload contains an unprotected paste end marker: %q", got)
	}

	got = string(encodeTaskInput(agentprovider.TUIProfile{PasteMode: agentprovider.PasteLiteral}, "a\nb"))
	if got != "a b" {
		t.Fatalf("literal payload = %q, want one line", got)
	}
}

func TestIsUserActivityIgnoresTerminalReplies(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"focus in", "\x1b[I", false},
		{"focus out", "\x1b[O", false},
		{"cursor position reply", "\x1b[12;40R", false},
		{"device attributes reply", "\x1b[?1;2c", false},
		{"kitty keyboard reply", "\x1b[?1u", false},
		{"mouse report", "\x1b[<0;10;20M", false},
		{"provider OSC", "\x1b]7501;state=idle:app=codex\x07", false},
		{"printable text", "hello", true},
		{"enter", "\r", true},
		{"ctrl-c", "\x03", true},
		{"arrow key", "\x1b[A", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUserActivity([]byte(tt.data)); got != tt.want {
				t.Errorf("isUserActivity(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestIsTerminalReplyOnly(t *testing.T) {
	tests := []struct {
		data string
		want bool
	}{
		{"\x1b[I", true},
		{"\x1b[12;40R", true},
		{"\x1b[?1;2c", true},
		{"\x1b[?1u", true},
		{"\x1b]10;rgb:ffff/ffff/ffff\x07", true},
		{"\x1b[<0;10;20M", false},
		{"\x1b[A", false},
		{"hello", false},
	}
	for _, tt := range tests {
		if got := isTerminalReplyOnly([]byte(tt.data)); got != tt.want {
			t.Errorf("isTerminalReplyOnly(%q) = %v, want %v", tt.data, got, tt.want)
		}
	}
}

func waitInputAction(t *testing.T, ch <-chan inputEvent, want inputAction) {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.action != want {
				continue
			}
			if ev.err != nil {
				t.Fatalf("input event %d failed: %v", want, ev.err)
			}
			return
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for input action %d", want)
		}
	}
}
