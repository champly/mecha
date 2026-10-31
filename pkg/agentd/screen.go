package agentd

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image/color"
	"strings"
	"sync"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/charmbracelet/x/vt"
)

// ScreenParser converts ANSI output into immutable terminal snapshots.
type ScreenParser interface {
	Write([]byte) error
	Resize(rows, cols int) error
	Snapshot() agentprovider.ScreenSnapshot
	Responses() <-chan []byte
}

type vtScreenParser struct {
	mu             sync.Mutex
	emu            *vt.Emulator
	version        uint64
	bracketedPaste bool
	modeTail       []byte
	oscTail        []byte
	appID          string
	appState       string
	responses      chan []byte
}

func newScreenParser(rows, cols int) *vtScreenParser {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	emu := vt.NewEmulator(cols, rows)
	emu.SetDefaultForegroundColor(color.Black)
	emu.SetDefaultBackgroundColor(color.White)
	emu.SetDefaultCursorColor(color.Black)
	p := &vtScreenParser{
		emu:       emu,
		responses: make(chan []byte, 16),
	}
	go p.drainResponses()
	return p
}

func (s *vtScreenParser) Write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.emu.Write(p); err != nil {
		return fmt.Errorf("screen parser: %w", err)
	}
	s.trackModes(p)
	s.trackOSC(p)
	s.version++
	return nil
}

func (s *vtScreenParser) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("screen parser: invalid size %dx%d", cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emu.Resize(cols, rows)
	s.version++
	return nil
}

func (s *vtScreenParser) Snapshot() agentprovider.ScreenSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows := make([]string, 0, s.emu.Height())
	for y := 0; y < s.emu.Height(); y++ {
		var b strings.Builder
		for x := 0; x < s.emu.Width(); x++ {
			cell := s.emu.CellAt(x, y)
			if cell == nil || cell.Content == "" {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(cell.Content)
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	cursor := s.emu.CursorPosition()
	hash := fnv.New64a()
	for _, row := range rows {
		_, _ = hash.Write([]byte(row))
		_, _ = hash.Write([]byte{'\n'})
	}
	return agentprovider.ScreenSnapshot{
		Version:        s.version,
		Rows:           rows,
		CursorX:        cursor.X,
		CursorY:        cursor.Y,
		Alternate:      s.emu.IsAltScreen(),
		BracketedPaste: s.bracketedPaste,
		AppID:          s.appID,
		AppState:       s.appState,
		ContentHash:    hash.Sum64(),
	}
}

func (s *vtScreenParser) Responses() <-chan []byte {
	return s.responses
}

// drainResponses keeps the emulator's internal input pipe empty. Without this,
// terminal queries such as DSR/DA can block Emulator.Write forever.
func (s *vtScreenParser) drainResponses() {
	defer close(s.responses)
	buf := make([]byte, 4096)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 {
			resp := append([]byte(nil), buf[:n]...)
			select {
			case s.responses <- resp:
			default:
				// The outer terminal may already answer the same query.
			}
		}
		if err != nil {
			return
		}
	}
}

// trackModes follows terminal modes that affect task injection. Sequences may
// arrive split across arbitrary PTY reads, so a short tail is carried forward.
func (s *vtScreenParser) trackModes(p []byte) {
	data := append(append([]byte(nil), s.modeTail...), p...)
	for i := 0; i+3 < len(data); i++ {
		if data[i] != 0x1b || data[i+1] != '[' || data[i+2] != '?' {
			continue
		}
		j := i + 3
		for j < len(data) && (data[j] == ';' || (data[j] >= '0' && data[j] <= '9')) {
			j++
		}
		if j >= len(data) || (data[j] != 'h' && data[j] != 'l') {
			continue
		}
		for _, mode := range bytes.Split(data[i+3:j], []byte(";")) {
			if bytes.Equal(mode, []byte("2004")) {
				s.bracketedPaste = data[j] == 'h'
			}
		}
		i = j
	}
	const modeTailSize = 32
	if len(data) > modeTailSize {
		data = data[len(data)-modeTailSize:]
	}
	s.modeTail = append(s.modeTail[:0], data...)
}

// trackOSC follows provider lifecycle status emitted as OSC sequences. Codex
// currently exposes OSC 7501 with state=idle|working and app=codex.
func (s *vtScreenParser) trackOSC(p []byte) {
	data := append(append([]byte(nil), s.oscTail...), p...)
	var incomplete []byte
	for i := 0; i+1 < len(data); {
		if data[i] != 0x1b || data[i+1] != ']' {
			i++
			continue
		}
		end := -1
		terminatorLen := 0
		for j := i + 2; j < len(data); j++ {
			if data[j] == 0x07 {
				end = j
				terminatorLen = 1
				break
			}
			if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
				end = j
				terminatorLen = 2
				break
			}
		}
		if end < 0 {
			incomplete = data[i:]
			break
		}
		s.handleOSC(string(data[i+2 : end]))
		i = end + terminatorLen
	}
	const oscTailSize = 4096
	if len(incomplete) > oscTailSize {
		incomplete = incomplete[len(incomplete)-oscTailSize:]
	}
	s.oscTail = append(s.oscTail[:0], incomplete...)
}

func (s *vtScreenParser) handleOSC(payload string) {
	if !strings.HasPrefix(payload, "7501;") {
		return
	}
	for _, field := range strings.Split(strings.TrimPrefix(payload, "7501;"), ":") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "state":
			s.appState = value
		case "app":
			s.appID = value
		}
	}
}
