package agentd

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/api"
	"github.com/creack/pty"
	"golang.org/x/term"
)

type sessionTestHost struct {
	ptmx       *os.File
	tty        *os.File
	commands   chan inputCommand
	statuses   chan agentprovider.HookEvent
	failReason chan string
	userActive bool
}

func newSessionTestHost(t *testing.T, raw bool) (*sessionActor, *sessionTestHost) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = tty.Close()
	})
	if raw {
		old, err := term.MakeRaw(int(tty.Fd()))
		if err != nil {
			t.Fatalf("MakeRaw: %v", err)
		}
		t.Cleanup(func() { _ = term.Restore(int(tty.Fd()), old) })
	}

	th := &sessionTestHost{
		ptmx:       ptmx,
		tty:        tty,
		commands:   make(chan inputCommand, 32),
		statuses:   make(chan agentprovider.HookEvent, 8),
		failReason: make(chan string, 8),
	}
	input := &inputArbiter{commands: th.commands}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	s := newSessionActor(sessionHost{
		profile: agentprovider.TUIProfile{
			PasteMode: agentprovider.PasteBracketed,
			SubmitKey: []byte("\r"),
			Classify: func(s agentprovider.ScreenSnapshot) agentprovider.Classification {
				text := strings.Join(s.Rows, "\n")
				switch {
				case strings.Contains(text, "BLOCKED"):
					return agentprovider.Classification{State: agentprovider.InputBlocked, Reason: "permission required"}
				case strings.Contains(text, "BUSY"):
					return agentprovider.Classification{State: agentprovider.InputBusy, Reason: "busy"}
				case strings.Contains(text, "DRAFT"):
					return agentprovider.Classification{State: agentprovider.InputDraft}
				case strings.Contains(text, "EMPTY"):
					return agentprovider.Classification{State: agentprovider.InputEmpty}
				default:
					return agentprovider.Classification{State: agentprovider.InputUnknown}
				}
			},
		},
		input:  input,
		ptmx:   func() *os.File { return th.ptmx },
		logger: slog.New(slog.DiscardHandler),
		report: func(status, reason string) {
			th.statuses <- agentprovider.HookEvent{Event: status, Prompt: reason}
		},
		fail: func(reason string) { th.failReason <- reason },
		userActive: func() bool {
			return th.userActive
		},
		clearUserActive: func() { th.userActive = false },
	}, stop, sessionTiming{
		Settle:       time.Millisecond,
		Boot:         time.Second,
		Queue:        time.Second,
		Render:       time.Second,
		Confirmation: time.Second,
	})
	return s, th
}

func snapshot(version uint64, text string) agentprovider.ScreenSnapshot {
	return agentprovider.ScreenSnapshot{Version: version, Rows: []string{text}}
}

func requireNoCommand(t *testing.T, ch <-chan inputCommand) {
	t.Helper()
	select {
	case cmd := <-ch:
		t.Fatalf("unexpected PTY command: action=%d data=%q", cmd.action, cmd.data)
	case <-time.After(10 * time.Millisecond):
	}
}

func requireCommand(t *testing.T, ch <-chan inputCommand, want inputAction) inputCommand {
	t.Helper()
	select {
	case cmd := <-ch:
		if cmd.action != want {
			t.Fatalf("command action = %d, want %d", cmd.action, want)
		}
		return cmd
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for input command %d", want)
		return inputCommand{}
	}
}

func requireResult(t *testing.T, ch <-chan taskResult) taskResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result")
		return taskResult{}
	}
}

func TestSessionDoesNotDispatchBeforeReady(t *testing.T) {
	s, host := newSessionTestHost(t, false)
	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	requireNoCommand(t, host.commands)
	if s.started {
		t.Fatal("session reported ready without raw terminal mode")
	}
}

func TestSessionUnknownIsNeverReady(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "UNRECOGNIZED"))
	s.handleTimer(timerSettle)

	if s.started {
		t.Fatal("session reported ready from an unknown screen")
	}
	requireNoCommand(t, host.commands)
}

func TestSessionSettleUsesVisibleContentHash(t *testing.T) {
	s, _ := newSessionTestHost(t, true)
	first := snapshot(1, "EMPTY")
	first.ContentHash = 100
	s.handleScreen(first)
	firstTimer := s.timerIDs[timerSettle]

	sameContent := snapshot(2, "EMPTY")
	sameContent.ContentHash = 100
	s.handleScreen(sameContent)
	if got := s.timerIDs[timerSettle]; got != firstTimer {
		t.Fatalf("style-only screen update reset settle timer: %d -> %d", firstTimer, got)
	}

	changed := snapshot(3, "EMPTY")
	changed.ContentHash = 200
	s.handleScreen(changed)
	if got := s.timerIDs[timerSettle]; got == firstTimer {
		t.Fatal("visible content change did not restart settle timer")
	}
}

func TestSessionSettleRechecksRawMode(t *testing.T) {
	s, host := newSessionTestHost(t, false)
	s.handleScreen(snapshot(1, "EMPTY"))
	if _, err := term.MakeRaw(int(host.tty.Fd())); err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	s.handleTimer(timerSettle)
	if !s.started {
		t.Fatal("settle did not recognize raw mode after the input box appeared")
	}
}

func TestSessionQueuesTaskWhileUserIsActive(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)
	host.userActive = true

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireNoCommand(t, host.commands)

	host.userActive = false
	s.handleScreen(snapshot(2, "EMPTY"))
	requireCommand(t, host.commands, inputTaskText)
}

func TestSessionQueueTimeoutFailsWithReason(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)
	host.userActive = true

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	s.handleTimer(timerQueue)
	result := requireResult(t, p.resultCh)
	if !strings.Contains(result.result, "queue timeout") {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionReadySubmitAndTurnResult(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)
	if !s.started || s.phase != phaseIdle {
		t.Fatalf("session state after ready = started:%v phase:%s", s.started, s.phase)
	}

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	paste := requireCommand(t, host.commands, inputTaskText)
	if !strings.Contains(string(paste.data), "do work") {
		t.Fatalf("paste data = %q", paste.data)
	}

	s.handleInput(inputEvent{action: inputTaskText})
	s.handleScreen(snapshot(2, "DRAFT do work"))
	requireCommand(t, host.commands, inputTaskEnter)
	s.handleInput(inputEvent{action: inputTaskEnter})

	s.handleHook(agentprovider.HookEvent{
		Event:  agentprovider.EventPromptSubmit,
		TurnID: "turn-1",
		Prompt: "do work",
	})
	if s.phase != phaseRunning || s.owner != ownerCore || s.current.turnID != "turn-1" {
		t.Fatalf("core turn state = phase:%s owner:%d turn:%q", s.phase, s.owner, s.current.turnID)
	}

	s.handleHook(agentprovider.HookEvent{
		Event:  agentprovider.EventStop,
		TurnID: "turn-1",
		Output: "done",
	})
	result := requireResult(t, p.resultCh)
	if !result.success || result.result != "done" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionUsesPostPasteFrameWhenProviderStaysIdle(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)

	postPaste := snapshot(2, "EMPTY")
	s.handleInput(inputEvent{action: inputTaskText, snapshot: &postPaste})
	requireCommand(t, host.commands, inputTaskEnter)
}

func TestSessionStopBeforePromptSubmitIsBoundByTurn(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)
	s.handleInput(inputEvent{action: inputTaskText})
	s.handleScreen(snapshot(2, "DRAFT"))
	requireCommand(t, host.commands, inputTaskEnter)

	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventStop, TurnID: "turn-1", Output: "early"})
	select {
	case result := <-p.resultCh:
		t.Fatalf("Stop was delivered before turn binding: %+v", result)
	default:
	}

	s.handleHook(agentprovider.HookEvent{
		Event:  agentprovider.EventPromptSubmit,
		TurnID: "turn-1",
		Prompt: "do work",
	})
	result := requireResult(t, p.resultCh)
	if !result.success || result.result != "early" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionIgnoresMismatchedTurn(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)
	s.handleInput(inputEvent{action: inputTaskText})
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventPromptSubmit, TurnID: "core-turn", Prompt: "do work"})

	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventStop, TurnID: "user-turn", Output: "wrong"})
	select {
	case result := <-p.resultCh:
		t.Fatalf("mismatched turn delivered result: %+v", result)
	default:
	}

	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventStop, TurnID: "core-turn", Output: "right"})
	if got := requireResult(t, p.resultCh); got.result != "right" {
		t.Fatalf("result = %+v", got)
	}
}

func TestUserStopIsNotDeliveredToCore(t *testing.T) {
	s, _ := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	s.handleHook(agentprovider.HookEvent{
		Event:  agentprovider.EventPromptSubmit,
		TurnID: "user-turn",
		Prompt: "user question",
	})
	if s.owner != ownerUser || s.phase != phaseRunning {
		t.Fatalf("user turn state = phase:%s owner:%d", s.phase, s.owner)
	}
	if s.current != nil {
		t.Fatal("user prompt created a Core pending task")
	}
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventStop, TurnID: "user-turn", Output: "user answer"})
	if s.phase != phaseIdle || s.owner != ownerNone {
		t.Fatalf("state after user Stop = phase:%s owner:%d", s.phase, s.owner)
	}
}

func TestBlockedScreenFailsRunningCoreTurn(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)
	s.handleInput(inputEvent{action: inputTaskText})
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventPromptSubmit, TurnID: "core-turn", Prompt: "do work"})

	s.handleScreen(snapshot(2, "BLOCKED"))
	result := requireResult(t, p.resultCh)
	if !strings.Contains(result.result, "interaction required") {
		t.Fatalf("result = %+v", result)
	}
	if s.phase != phaseInteractionRequired {
		t.Fatalf("phase = %s, want interaction-required", s.phase)
	}
}

func TestInterruptFailsMatchingCoreTurn(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)
	s.handleInput(inputEvent{action: inputTaskText})
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventPromptSubmit, TurnID: "core-turn", Prompt: "do work"})
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventInterrupt, TurnID: "core-turn"})

	result := requireResult(t, p.resultCh)
	if !strings.Contains(result.result, "interrupted by user") {
		t.Fatalf("result = %+v", result)
	}
}

func TestBusyDisappearanceFailsCoreTurnWithoutStop(t *testing.T) {
	s, host := newSessionTestHost(t, true)
	s.handleScreen(snapshot(1, "EMPTY"))
	s.handleTimer(timerSettle)

	p := &pendingTask{
		request:  &api.TaskRequest{Id: "t1", Task: "do work"},
		resultCh: make(chan taskResult, 1),
		prompt:   "do work",
	}
	s.handleTask(p)
	requireCommand(t, host.commands, inputTaskText)
	s.handleInput(inputEvent{action: inputTaskText})
	s.handleHook(agentprovider.HookEvent{Event: agentprovider.EventPromptSubmit, TurnID: "core-turn", Prompt: "do work"})

	s.handleScreen(snapshot(2, "BUSY"))
	s.handleScreen(snapshot(3, "EMPTY"))
	s.handleTimer(timerInterrupt)
	result := requireResult(t, p.resultCh)
	if !strings.Contains(result.result, "interrupted") {
		t.Fatalf("result = %+v", result)
	}
}
