package agentd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestScreenParserSnapshotAndModes(t *testing.T) {
	p := newScreenParser(5, 30)
	if err := p.Write([]byte("\x1b[?2004h\x1b[2J\x1b[1;1H› hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	s := p.Snapshot()
	if !s.BracketedPaste {
		t.Error("bracketed paste mode was not tracked")
	}
	if !strings.Contains(strings.Join(s.Rows, "\n"), "› hello") {
		t.Errorf("snapshot rows do not contain rendered text: %#v", s.Rows)
	}
	if s.Version == 0 {
		t.Error("snapshot version was not advanced")
	}
}

func TestScreenParserContentHashIgnoresStyleOnlyUpdates(t *testing.T) {
	p := newScreenParser(5, 30)
	if err := p.Write([]byte("hello")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	first := p.Snapshot().ContentHash
	if err := p.Write([]byte("\x1b[31m\x1b[?25l")); err != nil {
		t.Fatalf("Write style: %v", err)
	}
	if got := p.Snapshot().ContentHash; got != first {
		t.Fatalf("style-only update changed content hash: %x -> %x", first, got)
	}
	if err := p.Write([]byte(" world")); err != nil {
		t.Fatalf("Write more text: %v", err)
	}
	if got := p.Snapshot().ContentHash; got == first {
		t.Fatal("text update did not change content hash")
	}
}

func TestScreenParserTracksSplitModeSequence(t *testing.T) {
	p := newScreenParser(5, 30)
	if err := p.Write([]byte("\x1b[?20")); err != nil {
		t.Fatalf("Write part 1: %v", err)
	}
	if err := p.Write([]byte("04h")); err != nil {
		t.Fatalf("Write part 2: %v", err)
	}
	if !p.Snapshot().BracketedPaste {
		t.Error("split bracketed paste sequence was not tracked")
	}
	if err := p.Write([]byte("\x1b[?2004l")); err != nil {
		t.Fatalf("Write disable: %v", err)
	}
	if p.Snapshot().BracketedPaste {
		t.Error("bracketed paste disable sequence was not tracked")
	}
}

func TestScreenParserTracksProviderStateOSC(t *testing.T) {
	p := newScreenParser(5, 30)
	if err := p.Write([]byte("\x1b]7501;state=wo")); err != nil {
		t.Fatalf("Write part 1: %v", err)
	}
	if err := p.Write([]byte("rking:app=codex\x07")); err != nil {
		t.Fatalf("Write part 2: %v", err)
	}
	s := p.Snapshot()
	if s.AppID != "codex" || s.AppState != "working" {
		t.Fatalf("provider state = app:%q state:%q", s.AppID, s.AppState)
	}

	if err := p.Write([]byte("\x1b]7501;state=idle:app=codex\x1b\\")); err != nil {
		t.Fatalf("Write ST terminator: %v", err)
	}
	if got := p.Snapshot().AppState; got != "idle" {
		t.Fatalf("provider state = %q, want idle", got)
	}
}

func TestScreenParserDrainsTerminalQueries(t *testing.T) {
	p := newScreenParser(5, 30)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Write([]byte("\x1b[6n"))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("screen parser blocked on terminal query response")
	}
	select {
	case response := <-p.Responses():
		if len(response) == 0 {
			t.Fatal("empty terminal query response")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal query response was not produced")
	}
}

func TestPTYRawProbe(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	raw, err := ptyRaw(ptmx)
	if err != nil {
		t.Fatalf("ptyRaw canonical: %v", err)
	}
	if raw {
		t.Fatal("new PTY unexpectedly reports raw mode")
	}
	old, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	defer term.Restore(int(tty.Fd()), old)

	raw, err = ptyRaw(ptmx)
	if err != nil {
		t.Fatalf("ptyRaw raw: %v", err)
	}
	if !raw {
		t.Fatal("PTY did not report raw mode")
	}
}

func TestPTYRawProbeClosed(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-pty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if _, err := ptyRaw(f); err == nil {
		t.Fatal("ptyRaw accepted a regular file")
	}
}
