package agentd

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
)

const maxBufferedUserInput = 1 << 20 // 1 MiB

type inputAction uint8

const (
	inputUser inputAction = iota
	inputSystem
	inputTaskText
	inputTaskEnter
	inputEndTask
)

type inputCommand struct {
	action inputAction
	data   []byte
	abort  bool
}

type inputEvent struct {
	action   inputAction
	err      error
	overflow bool
	snapshot *agentprovider.ScreenSnapshot
}

// inputArbiter is the only component that writes to the agent PTY. User bytes
// arriving during a task transaction are buffered and flushed at the boundary.
type inputArbiter struct {
	commands chan inputCommand
	events   chan<- inputEvent
	ptmx     func() *os.File
	stop     <-chan struct{}

	transaction bool
	userBuffer  []byte
	overflowed  bool
}

func newInputArbiter(ptmx func() *os.File, events chan<- inputEvent, stop <-chan struct{}) *inputArbiter {
	return &inputArbiter{
		commands: make(chan inputCommand, 64),
		events:   events,
		ptmx:     ptmx,
		stop:     stop,
	}
}

func (a *inputArbiter) send(cmd inputCommand) bool {
	select {
	case a.commands <- cmd:
		return true
	case <-a.stop:
		return false
	default:
		return false
	}
}

func (a *inputArbiter) user(data []byte) bool {
	return a.send(inputCommand{action: inputUser, data: append([]byte(nil), data...)})
}

func (a *inputArbiter) system(data []byte) bool {
	return a.send(inputCommand{action: inputSystem, data: append([]byte(nil), data...)})
}

func (a *inputArbiter) taskText(profile agentprovider.TUIProfile, text string) bool {
	return a.send(inputCommand{
		action: inputTaskText,
		data:   encodeTaskInput(profile, text),
	})
}

func (a *inputArbiter) taskEnter(profile agentprovider.TUIProfile) bool {
	return a.send(inputCommand{
		action: inputTaskEnter,
		data:   append([]byte(nil), profile.SubmitKey...),
	})
}

func (a *inputArbiter) endTask(abort bool) bool {
	return a.send(inputCommand{action: inputEndTask, abort: abort})
}

func (a *inputArbiter) run() {
	for {
		select {
		case <-a.stop:
			return
		case cmd := <-a.commands:
			a.handle(cmd)
		}
	}
}

func (a *inputArbiter) handle(cmd inputCommand) {
	switch cmd.action {
	case inputUser:
		a.handleUser(cmd.data)
	case inputSystem:
		a.writeCommand(cmd)
	case inputTaskText:
		a.transaction = true
		a.writeCommand(cmd)
	case inputTaskEnter:
		a.writeCommand(cmd)
	case inputEndTask:
		a.endTransaction(cmd.abort)
	}
}

func (a *inputArbiter) handleUser(data []byte) {
	if a.transaction {
		if a.overflowed {
			return
		}
		if len(a.userBuffer)+len(data) > maxBufferedUserInput {
			a.overflowed = true
			a.emit(inputEvent{action: inputUser, overflow: true, err: fmt.Errorf("input buffer exceeded %d bytes", maxBufferedUserInput)})
			return
		}
		a.userBuffer = append(a.userBuffer, data...)
		return
	}
	err := a.writeAll(data)
	a.emit(inputEvent{action: inputUser, err: err})
}

func (a *inputArbiter) writeCommand(cmd inputCommand) {
	err := a.writeAll(cmd.data)
	a.emit(inputEvent{action: cmd.action, err: err})
}

func (a *inputArbiter) endTransaction(abort bool) {
	a.transaction = false
	a.overflowed = false
	if abort {
		a.userBuffer = a.userBuffer[:0]
		return
	}
	var err error
	if len(a.userBuffer) > 0 {
		err = a.writeAll(a.userBuffer)
		a.userBuffer = a.userBuffer[:0]
	}
	a.emit(inputEvent{action: inputEndTask, err: err})
}

func (a *inputArbiter) writeAll(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	ptmx := a.ptmx()
	if ptmx == nil {
		return fmt.Errorf("agent pty is closed")
	}
	for len(data) > 0 {
		n, err := ptmx.Write(data)
		if err != nil {
			if err == io.ErrClosedPipe {
				return fmt.Errorf("agent pty is closed")
			}
			return fmt.Errorf("write agent pty: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("write agent pty: short write")
		}
		data = data[n:]
	}
	return nil
}

func (a *inputArbiter) emit(ev inputEvent) {
	select {
	case a.events <- ev:
	case <-a.stop:
	default:
		slog.Warn("agentd: input event queue full", "action", ev.action)
	}
}

func encodeTaskInput(profile agentprovider.TUIProfile, task string) []byte {
	text := agentprovider.NormalizePrompt(task)
	if profile.PasteMode != agentprovider.PasteBracketed {
		text = singleLineTask(text)
		return []byte(text)
	}
	text = escapeBracketedPaste(text)
	return []byte(bracketedPasteStart + text + bracketedPasteEnd)
}

func singleLineTask(task string) string {
	out := make([]byte, 0, len(task))
	for i := 0; i < len(task); i++ {
		switch task[i] {
		case '\r', '\n':
			out = append(out, ' ')
		default:
			out = append(out, task[i])
		}
	}
	return string(out)
}

func escapeBracketedPaste(s string) string {
	// A literal end marker inside user-controlled task text could terminate the
	// paste early. Strip the ESC byte and leave an inert text sequence.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if i+5 < len(s) && s[i:i+6] == bracketedPasteEnd {
			out = append(out, '?')
			i += 5
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// isUserActivity distinguishes keyboard input from terminal replies such as
// focus reports, cursor-position responses, device attributes, and kitty
// keyboard-mode replies. The bytes are still forwarded; this only controls
// whether automation treats the pane as user-owned.
func isUserActivity(p []byte) bool {
	for i := 0; i < len(p); {
		if p[i] != 0x1b {
			switch {
			case p[i] < 0x20, p[i] == 0x7f, p[i] == 0x08:
				return true
			case p[i] >= 0x20:
				return true
			}
			i++
			continue
		}
		if i+1 >= len(p) {
			return true
		}
		switch p[i+1] {
		case ']': // OSC reply (color, title, provider status)
			end := oscEnd(p, i+2)
			if end < 0 {
				return false
			}
			i = end
		case 'P', '_', '^': // DCS/APC/PM replies
			end := stringEnd(p, i+2)
			if end < 0 {
				return false
			}
			i = end
		case '[':
			if i+2 < len(p) && (p[i+2] == 'I' || p[i+2] == 'O') {
				i += 3
				continue
			}
			end := csiEnd(p, i+2)
			if end < 0 {
				return false
			}
			final := p[end-1]
			params := p[i+2 : end-1]
			isReply := final == 'R' ||
				((final == 'c' || final == 'u') &&
					len(params) > 0 && (params[0] == '?' || params[0] == '>'))
			isReply = isReply || ((final == 'M' || final == 'm') && len(params) > 0 && params[0] == '<')
			if !isReply {
				return true // arrows, editing keys, page keys, ...
			}
			i = end
		default:
			return true
		}
	}
	return false
}

func isTerminalReplyOnly(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	for i := 0; i < len(p); {
		if p[i] != 0x1b || i+1 >= len(p) {
			return false
		}
		switch p[i+1] {
		case ']':
			end := oscEnd(p, i+2)
			if end < 0 {
				return false
			}
			i = end
		case 'P', '_', '^':
			end := stringEnd(p, i+2)
			if end < 0 {
				return false
			}
			i = end
		case '[':
			if i+2 < len(p) && (p[i+2] == 'I' || p[i+2] == 'O') {
				i += 3
				continue
			}
			end := csiEnd(p, i+2)
			if end < 0 {
				return false
			}
			final := p[end-1]
			params := p[i+2 : end-1]
			isReply := final == 'R' ||
				((final == 'c' || final == 'u') &&
					len(params) > 0 && (params[0] == '?' || params[0] == '>'))
			if !isReply {
				return false
			}
			i = end
		default:
			return false
		}
	}
	return true
}

func oscEnd(p []byte, start int) int {
	for i := start; i < len(p); i++ {
		if p[i] == 0x07 {
			return i + 1
		}
		if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}

func stringEnd(p []byte, start int) int {
	for i := start; i+1 < len(p); i++ {
		if p[i] == 0x1b && p[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}

func csiEnd(p []byte, start int) int {
	for i := start; i < len(p); i++ {
		if p[i] >= 0x40 && p[i] <= 0x7e {
			return i + 1
		}
	}
	return -1
}
