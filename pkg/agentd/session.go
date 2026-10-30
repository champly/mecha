package agentd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/api"
)

type sessionPhase uint8

const (
	phaseBooting sessionPhase = iota
	phaseIdle
	phaseSubmitting
	phaseRunning
	phaseInteractionRequired
	phaseStartupFailed
	phaseExited
)

func (p sessionPhase) String() string {
	switch p {
	case phaseBooting:
		return "booting"
	case phaseIdle:
		return "idle"
	case phaseSubmitting:
		return "submitting"
	case phaseRunning:
		return "running"
	case phaseInteractionRequired:
		return "interaction-required"
	case phaseStartupFailed:
		return "startup-failed"
	case phaseExited:
		return "exited"
	default:
		return "unknown"
	}
}

type turnOwner uint8

const (
	ownerNone turnOwner = iota
	ownerCore
	ownerUser
)

type pendingTask struct {
	request   *api.TaskRequest
	resultCh  chan taskResult
	prompt    string
	turnID    string
	stopEarly *agentprovider.HookEvent
}

type sessionTiming struct {
	Settle       time.Duration
	Boot         time.Duration
	Queue        time.Duration
	Render       time.Duration
	Confirmation time.Duration
	Interrupt    time.Duration
}

var defaultSessionTiming = sessionTiming{
	Settle:       300 * time.Millisecond,
	Boot:         30 * time.Second,
	Queue:        10 * time.Minute,
	Render:       time.Second,
	Confirmation: 3 * time.Second,
	Interrupt:    10 * time.Second,
}

type sessionTimerKind uint8

const (
	timerSettle sessionTimerKind = iota
	timerBoot
	timerQueue
	timerRender
	timerConfirmation
	timerInterrupt
)

type sessionEventKind uint8

const (
	sessionTask sessionEventKind = iota
	sessionHook
	sessionUserActivity
	sessionInputResult
	sessionTimerFired
	sessionFatal
	sessionProcessExit
)

type sessionEvent struct {
	kind      sessionEventKind
	task      *pendingTask
	hook      agentprovider.HookEvent
	input     inputEvent
	timerKind sessionTimerKind
	timerID   uint64
	reason    string
	done      chan struct{}
}

type sessionHost struct {
	profile agentprovider.TUIProfile
	input   *inputArbiter
	ptmx    func() *os.File
	report  func(status, reason string)
	fail    func(reason string)
	logger  *slog.Logger

	userActive      func() bool
	clearUserActive func()
}

type sessionActor struct {
	host   sessionHost
	timing sessionTiming

	events  chan sessionEvent
	screens chan agentprovider.ScreenSnapshot
	stop    <-chan struct{}

	phase   sessionPhase
	owner   turnOwner
	started bool

	snapshot         agentprovider.ScreenSnapshot
	class            agentprovider.Classification
	lastLoggedInput  agentprovider.InputState
	lastLoggedReason string
	raw              bool
	dirty            bool
	dirtyFromVersion uint64

	blockedReason string
	queued        *pendingTask
	current       *pendingTask
	pasteDone     bool
	enterSent     bool
	enterAttempts int
	renderBase    uint64

	timerSeq uint64
	timerIDs map[sessionTimerKind]uint64

	settleHash   uint64
	settleClass  agentprovider.InputState
	settleReason string
	sawBusy      bool
}

func newSessionActor(host sessionHost, stop <-chan struct{}, timing sessionTiming) *sessionActor {
	if timing == (sessionTiming{}) {
		timing = defaultSessionTiming
	}
	return &sessionActor{
		host:     host,
		timing:   timing,
		events:   make(chan sessionEvent, 256),
		screens:  make(chan agentprovider.ScreenSnapshot, 1),
		stop:     stop,
		phase:    phaseBooting,
		timerIDs: make(map[sessionTimerKind]uint64),
	}
}

func (s *sessionActor) run() {
	s.schedule(timerBoot, s.timing.Boot)
	for {
		select {
		case <-s.stop:
			return
		case snapshot := <-s.screens:
			s.handleScreen(snapshot)
		case ev := <-s.events:
			s.handleEvent(ev)
		}
	}
}

func (s *sessionActor) screenChanged(snapshot agentprovider.ScreenSnapshot) {
	// Only the newest frame matters. Spinner redraws must not flood the actor.
	select {
	case s.screens <- snapshot:
	default:
		select {
		case <-s.screens:
		default:
		}
		select {
		case s.screens <- snapshot:
		default:
		}
	}
}

func (s *sessionActor) enqueue(ev sessionEvent) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.stop:
		return false
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

func (s *sessionActor) enqueueTask(req *api.TaskRequest) (chan taskResult, bool) {
	p := &pendingTask{
		request:  req,
		resultCh: make(chan taskResult, 1),
		prompt:   agentprovider.NormalizePrompt(req.Task),
	}
	return p.resultCh, s.enqueue(sessionEvent{kind: sessionTask, task: p})
}

func (s *sessionActor) enqueueHook(ev agentprovider.HookEvent) bool {
	return s.enqueue(sessionEvent{kind: sessionHook, hook: ev})
}

func (s *sessionActor) enqueueUserActivity() bool {
	return s.enqueue(sessionEvent{kind: sessionUserActivity})
}

func (s *sessionActor) enqueueInput(ev inputEvent) bool {
	return s.enqueue(sessionEvent{kind: sessionInputResult, input: ev})
}

func (s *sessionActor) enqueueExit(reason string) {
	done := make(chan struct{})
	select {
	case s.events <- sessionEvent{kind: sessionProcessExit, reason: reason, done: done}:
		select {
		case <-done:
		case <-time.After(time.Second):
		case <-s.stop:
		}
	case <-s.stop:
	case <-time.After(time.Second):
	}
}

func (s *sessionActor) enqueueFatal(reason string) bool {
	return s.enqueue(sessionEvent{kind: sessionFatal, reason: reason})
}

func (s *sessionActor) schedule(kind sessionTimerKind, d time.Duration) {
	s.timerSeq++
	id := s.timerSeq
	s.timerIDs[kind] = id
	time.AfterFunc(d, func() {
		select {
		case s.events <- sessionEvent{kind: sessionTimerFired, timerKind: kind, timerID: id}:
		case <-s.stop:
		}
	})
}

func (s *sessionActor) cancelTimer(kind sessionTimerKind) {
	delete(s.timerIDs, kind)
}

func (s *sessionActor) handleEvent(ev sessionEvent) {
	switch ev.kind {
	case sessionTask:
		s.handleTask(ev.task)
	case sessionHook:
		s.handleHook(ev.hook)
	case sessionUserActivity:
		s.dirty = true
		s.dirtyFromVersion = s.snapshot.Version
		s.cancelTimer(timerSettle)
	case sessionInputResult:
		s.handleInput(ev.input)
	case sessionTimerFired:
		if ev.timerKind == timerSettle {
			s.drainScreens()
		}
		if s.timerIDs[ev.timerKind] != ev.timerID {
			return
		}
		s.cancelTimer(ev.timerKind)
		s.handleTimer(ev.timerKind)
	case sessionFatal:
		s.startupFailed(ev.reason)
	case sessionProcessExit:
		s.handleProcessExit(ev.reason)
		if ev.done != nil {
			close(ev.done)
		}
	}
}

func (s *sessionActor) drainScreens() {
	for {
		select {
		case snapshot := <-s.screens:
			s.handleScreen(snapshot)
		default:
			return
		}
	}
}

func (s *sessionActor) handleTask(p *pendingTask) {
	if s.phase == phaseStartupFailed || s.phase == phaseExited {
		s.deliver(p, taskResult{result: "agent is not running"})
		return
	}
	if s.queued != nil {
		// taskLoop is serial, so this is only possible during shutdown races.
		s.deliver(p, taskResult{result: "agent is already handling a queued task"})
		return
	}
	s.queued = p
	s.schedule(timerQueue, s.timing.Queue)
	s.maybeDispatch()
}

func (s *sessionActor) handleHook(ev agentprovider.HookEvent) {
	s.host.logger.Debug("agentd: hook",
		"event", ev.Event,
		"turn", ev.TurnID,
		"source", ev.Source,
		"phase", s.phase.String(),
	)
	switch ev.Event {
	case agentprovider.EventPromptSubmit:
		s.handlePromptSubmit(ev)
	case agentprovider.EventStop:
		s.handleStop(ev, true)
	case agentprovider.EventStopFailure:
		s.handleStop(ev, false)
	case agentprovider.EventInterrupt:
		s.handleInterrupt(ev)
	case agentprovider.EventSessionStart:
		s.handleSessionStart(ev)
	case agentprovider.EventInteraction:
		if interactionNotification(ev) {
			reason := strings.TrimSpace(ev.Message)
			if reason == "" {
				reason = ev.NotificationType
			}
			if reason == "" {
				reason = "agent requires user interaction"
			}
			s.enterBlocked(reason)
		}
	}
}

func (s *sessionActor) handlePromptSubmit(ev agentprovider.HookEvent) {
	prompt := agentprovider.NormalizePrompt(ev.Prompt)
	if s.current != nil && prompt != "" && prompt == s.current.prompt {
		if ev.TurnID != "" {
			s.current.turnID = ev.TurnID
		}
		if s.current.stopEarly != nil && turnMatches(s.current, *s.current.stopEarly) {
			stop := *s.current.stopEarly
			s.current.stopEarly = nil
			s.completeCurrent(taskResult{success: true, result: stop.Output})
			return
		}
		s.startCoreTurn("prompt-submit")
		return
	}

	if s.current != nil {
		s.failCurrent(fmt.Sprintf("task dispatch was overtaken by a different prompt: %q", truncatePrompt(prompt)))
	}
	s.ensureStarted("user prompt")
	if ev.TurnID != "" && s.owner != ownerUser {
		s.current = nil
	}
	s.owner = ownerUser
	s.phase = phaseRunning
	s.sawBusy = false
	s.cancelTimer(timerInterrupt)
	s.cancelTimer(timerRender)
	s.cancelTimer(timerConfirmation)
	s.cancelTimer(timerSettle)
	s.host.input.endTask(false)
}

func (s *sessionActor) handleStop(ev agentprovider.HookEvent, success bool) {
	if s.phase == phaseRunning && s.owner == ownerUser {
		s.phase = phaseIdle
		s.owner = ownerNone
		s.sawBusy = false
		s.cancelTimer(timerInterrupt)
		s.maybeDispatch()
		return
	}
	if s.phase == phaseSubmitting && s.current != nil {
		copy := ev
		s.current.stopEarly = &copy
		if ev.TurnID != "" {
			s.current.turnID = ev.TurnID
		}
		return
	}
	if s.phase != phaseRunning || s.owner != ownerCore || s.current == nil {
		return
	}
	if !turnMatches(s.current, ev) {
		return
	}
	if s.current.turnID == "" && ev.TurnID != "" {
		s.current.turnID = ev.TurnID
	}
	if success {
		s.completeCurrent(taskResult{success: true, result: ev.Output})
	} else {
		message := ev.Error
		if message == "" {
			message = "agent turn failed"
		}
		s.failCurrent(message)
	}
}

func (s *sessionActor) handleInterrupt(ev agentprovider.HookEvent) {
	switch {
	case s.phase == phaseSubmitting && s.current != nil:
		if turnMatches(s.current, ev) {
			s.failCurrent("interrupted by user")
		}
	case s.phase == phaseRunning && s.owner == ownerCore && s.current != nil:
		if turnMatches(s.current, ev) {
			s.failCurrent("interrupted by user")
		}
	case s.phase == phaseRunning && s.owner == ownerUser:
		s.phase = phaseIdle
		s.owner = ownerNone
		s.sawBusy = false
		s.cancelTimer(timerInterrupt)
		s.maybeDispatch()
	}
}

func (s *sessionActor) handleSessionStart(ev agentprovider.HookEvent) {
	if ev.Source != "clear" {
		return
	}
	if (s.phase == phaseSubmitting || s.phase == phaseRunning) && s.owner == ownerCore && s.current != nil {
		s.failCurrent("session was cleared")
	} else if s.owner == ownerUser {
		s.phase = phaseIdle
		s.owner = ownerNone
		s.sawBusy = false
		s.cancelTimer(timerInterrupt)
	}
}

func (s *sessionActor) handleScreen(snapshot agentprovider.ScreenSnapshot) {
	if snapshot.Version != 0 && snapshot.Version <= s.snapshot.Version {
		return
	}
	s.snapshot = snapshot
	s.class = s.host.profile.Classify(snapshot)
	s.probeTermios()
	if s.class.State != s.lastLoggedInput || s.class.Reason != s.lastLoggedReason {
		s.host.logger.Info("agentd: TUI input state",
			"state", s.class.State.String(),
			"reason", s.class.Reason,
			"raw", s.raw,
			"phase", s.phase.String(),
		)
		s.lastLoggedInput = s.class.State
		s.lastLoggedReason = s.class.Reason
	}

	if s.class.State == agentprovider.InputEmpty {
		if snapshot.Version > s.dirtyFromVersion {
			s.dirty = false
			if s.host.clearUserActive != nil {
				s.host.clearUserActive()
			}
		}
	}

	if s.class.State == agentprovider.InputBlocked {
		s.enterBlocked(reasonOr(s.class.Reason, "input blocked"))
		return
	}

	switch s.phase {
	case phaseBooting:
		s.maybeReady()
	case phaseIdle:
		s.maybeDispatch()
	case phaseSubmitting:
		s.advanceSubmission()
	case phaseRunning:
		s.handleRunningScreen()
	case phaseInteractionRequired:
		if s.raw && s.class.State == agentprovider.InputEmpty {
			s.blockedReason = ""
			s.phase = phaseIdle
			s.maybeReady()
			s.maybeDispatch()
		}
	}
}

func (s *sessionActor) handleInput(ev inputEvent) {
	if ev.overflow {
		s.failCurrentWithAbort(ev.err.Error())
		return
	}
	if ev.err != nil {
		if s.current != nil {
			s.failCurrent(ev.err.Error())
		} else {
			s.host.logger.Warn("agentd: input write failed", "err", ev.err)
			s.host.fail("input write failed: " + ev.err.Error())
		}
		return
	}

	switch ev.action {
	case inputTaskText:
		if s.phase != phaseSubmitting || s.current == nil {
			return
		}
		s.pasteDone = true
		if ev.snapshot != nil {
			s.handleScreen(*ev.snapshot)
		}
		s.advanceSubmission()
	case inputTaskEnter:
		if s.phase != phaseSubmitting || s.current == nil {
			return
		}
		s.schedule(timerConfirmation, s.timing.Confirmation)
	}
}

func (s *sessionActor) handleTimer(kind sessionTimerKind) {
	switch kind {
	case timerBoot:
		if s.phase == phaseBooting {
			reason := "input state not recognized"
			if s.blockedReason != "" {
				reason = "startup blocked: " + s.blockedReason
			}
			s.startupFailed(reason)
		}
	case timerSettle:
		if s.phase != phaseBooting && s.phase != phaseIdle {
			return
		}
		s.probeTermios()
		if !s.raw || s.class.State != agentprovider.InputEmpty || s.userDirty() {
			if s.phase == phaseBooting && s.class.State == agentprovider.InputEmpty && !s.userDirty() {
				s.schedule(timerSettle, s.timing.Settle)
			}
			return
		}
		if s.phase == phaseBooting {
			s.ensureStarted("stable input")
		}
		s.maybeDispatch()
	case timerQueue:
		if s.queued != nil {
			s.deliver(s.queued, taskResult{result: "agent remained occupied beyond queue timeout"})
			s.queued = nil
		}
	case timerRender:
		if s.phase == phaseSubmitting {
			s.failCurrent("task text did not reach the TUI input")
		}
	case timerConfirmation:
		if s.phase != phaseSubmitting || s.current == nil {
			return
		}
		if s.class.State == agentprovider.InputDraft && s.enterAttempts < 3 {
			s.enterAttempts++
			if !s.host.input.taskEnter(s.host.profile) {
				s.failCurrent("input arbiter is unavailable")
			}
			return
		}
		s.failCurrent("task submission was not acknowledged")
	case timerInterrupt:
		if s.phase != phaseRunning || !s.sawBusy || s.class.State == agentprovider.InputBusy {
			return
		}
		if s.owner == ownerCore && s.current != nil {
			s.failCurrent("turn ended without a result (interrupted?)")
			return
		}
		if s.owner == ownerUser {
			s.phase = phaseIdle
			s.owner = ownerNone
			s.sawBusy = false
			s.maybeDispatch()
		}
	}
}

func (s *sessionActor) handleRunningScreen() {
	if s.class.State == agentprovider.InputBusy {
		s.sawBusy = true
		s.cancelTimer(timerInterrupt)
		return
	}
	if !s.sawBusy || s.timerPending(timerInterrupt) {
		return
	}
	s.schedule(timerInterrupt, s.timing.Interrupt)
}

func (s *sessionActor) maybeReady() {
	if s.phase != phaseBooting && s.phase != phaseIdle {
		return
	}
	if s.class.State != agentprovider.InputEmpty || s.userDirty() {
		s.cancelTimer(timerSettle)
		return
	}
	if s.phase == phaseBooting {
		contentHash := s.snapshot.ContentHash
		if contentHash == 0 {
			contentHash = s.snapshot.Version
		}
		if s.timerPending(timerSettle) &&
			s.settleHash == contentHash &&
			s.settleClass == s.class.State &&
			s.settleReason == s.class.Reason {
			return
		}
		s.schedule(timerSettle, s.timing.Settle)
		s.settleHash = contentHash
		s.settleClass = s.class.State
		s.settleReason = s.class.Reason
		return
	}
	if !s.raw {
		return
	}
	s.ensureStarted("input ready")
}

func (s *sessionActor) timerPending(kind sessionTimerKind) bool {
	_, ok := s.timerIDs[kind]
	return ok
}

func (s *sessionActor) ensureStarted(reason string) {
	if s.started {
		if s.phase == phaseBooting {
			s.phase = phaseIdle
		}
		return
	}
	s.started = true
	if s.phase == phaseBooting {
		s.phase = phaseIdle
	}
	s.cancelTimer(timerBoot)
	s.host.logger.Info("agentd: TUI ready", "reason", reason)
	s.host.report(api.StatusStarted, "")
}

func (s *sessionActor) maybeDispatch() {
	if s.queued == nil || (s.phase != phaseIdle && s.phase != phaseBooting) {
		return
	}
	if !s.started || !s.raw || s.class.State != agentprovider.InputEmpty || s.userDirty() {
		return
	}
	s.current = s.queued
	s.queued = nil
	s.cancelTimer(timerQueue)
	s.phase = phaseSubmitting
	s.owner = ownerCore
	s.pasteDone = false
	s.enterSent = false
	s.enterAttempts = 0
	s.renderBase = s.snapshot.Version
	if !s.host.input.taskText(s.host.profile, s.current.request.Task) {
		s.failCurrent("input arbiter is unavailable")
		return
	}
	s.schedule(timerRender, s.timing.Render)
}

func (s *sessionActor) advanceSubmission() {
	if s.phase != phaseSubmitting || s.current == nil || !s.pasteDone {
		return
	}
	if s.snapshot.Version <= s.renderBase && s.class.State != agentprovider.InputBusy {
		return
	}
	switch s.class.State {
	case agentprovider.InputBusy:
		s.startCoreTurn("screen busy")
	case agentprovider.InputDraft:
		if !s.enterSent {
			s.sendEnter()
		}
	case agentprovider.InputBlocked:
		s.enterBlocked(reasonOr(s.class.Reason, "input blocked"))
	case agentprovider.InputEmpty, agentprovider.InputUnknown:
		// Some TUIs (notably Codex) keep their provider state at idle while a
		// draft exists. A screen frame newer than the task write is positive
		// evidence that the TUI consumed the paste, so Enter is no longer a
		// blind write. Without such a frame the render timeout fails closed.
		if !s.enterSent && s.snapshot.Version > s.renderBase {
			s.sendEnter()
		}
	}
}

func (s *sessionActor) sendEnter() {
	s.enterSent = true
	s.enterAttempts = 1
	s.cancelTimer(timerRender)
	if !s.host.input.taskEnter(s.host.profile) {
		s.failCurrent("input arbiter is unavailable")
		return
	}
	s.schedule(timerConfirmation, s.timing.Confirmation)
}

func (s *sessionActor) startCoreTurn(reason string) {
	if s.current == nil {
		return
	}
	s.phase = phaseRunning
	s.owner = ownerCore
	s.cancelTimer(timerBoot)
	s.cancelTimer(timerSettle)
	s.cancelTimer(timerRender)
	s.cancelTimer(timerConfirmation)
	s.cancelTimer(timerInterrupt)
	s.sawBusy = s.class.State == agentprovider.InputBusy
	s.ensureStarted(reason)
	s.host.input.endTask(false)
}

func (s *sessionActor) completeCurrent(result taskResult) {
	if s.current == nil {
		return
	}
	p := s.current
	s.current = nil
	s.owner = ownerNone
	s.phase = phaseIdle
	s.resetSubmission()
	s.cancelTimer(timerInterrupt)
	s.sawBusy = false
	s.deliver(p, result)
	s.host.input.endTask(false)
	s.maybeDispatch()
}

func (s *sessionActor) failCurrent(reason string) {
	s.failCurrentInput(reason, false)
}

func (s *sessionActor) failCurrentWithAbort(reason string) {
	s.failCurrentInput(reason, true)
}

func (s *sessionActor) failCurrentInput(reason string, abort bool) {
	if s.current == nil {
		return
	}
	p := s.current
	s.current = nil
	s.owner = ownerNone
	if s.phase != phaseInteractionRequired {
		s.phase = phaseIdle
	}
	s.resetSubmission()
	s.cancelTimer(timerInterrupt)
	s.sawBusy = false
	s.deliver(p, taskResult{result: reason})
	s.host.input.endTask(abort)
	s.maybeDispatch()
}

func (s *sessionActor) resetSubmission() {
	s.pasteDone = false
	s.enterSent = false
	s.enterAttempts = 0
	s.cancelTimer(timerRender)
	s.cancelTimer(timerConfirmation)
}

func (s *sessionActor) enterBlocked(reason string) {
	s.blockedReason = reason
	switch s.phase {
	case phaseBooting:
		s.cancelTimer(timerSettle)
	case phaseIdle:
		s.cancelTimer(timerSettle)
	case phaseSubmitting, phaseRunning:
		if s.owner == ownerCore && s.current != nil {
			s.phase = phaseInteractionRequired
			s.failCurrent("interaction required: " + reason)
		} else {
			s.phase = phaseInteractionRequired
		}
	}
}

func (s *sessionActor) handleProcessExit(reason string) {
	message := "agent exited"
	if reason != "" {
		message += ": " + reason
	}
	if s.current != nil {
		s.deliver(s.current, taskResult{result: message})
		s.current = nil
	}
	if s.queued != nil {
		s.deliver(s.queued, taskResult{result: message})
		s.queued = nil
	}
	s.owner = ownerNone
	s.phase = phaseExited
}

func (s *sessionActor) startupFailed(reason string) {
	if s.phase == phaseStartupFailed || s.phase == phaseExited {
		return
	}
	s.phase = phaseStartupFailed
	if s.current != nil {
		s.deliver(s.current, taskResult{result: reason})
		s.current = nil
	}
	if s.queued != nil {
		s.deliver(s.queued, taskResult{result: reason})
		s.queued = nil
	}
	s.host.report(api.StatusFailed, reason)
	s.host.fail(reason)
}

func (s *sessionActor) deliver(p *pendingTask, result taskResult) {
	if p == nil || p.resultCh == nil {
		return
	}
	select {
	case p.resultCh <- result:
	default:
		s.host.logger.Warn("agentd: duplicate or undeliverable task result", "task", p.request.GetId())
	}
}

func (s *sessionActor) probeTermios() {
	ptmx := s.host.ptmx()
	if ptmx == nil {
		s.raw = false
		return
	}
	raw, err := ptyRaw(ptmx)
	if err != nil {
		s.raw = false
		s.host.logger.Debug("agentd: termios probe failed", "err", err)
		return
	}
	s.raw = raw
}

func turnMatches(p *pendingTask, ev agentprovider.HookEvent) bool {
	if p == nil {
		return false
	}
	if p.turnID == "" || ev.TurnID == "" {
		return true
	}
	return p.turnID == ev.TurnID
}

func (s *sessionActor) userDirty() bool {
	if s.dirty {
		return true
	}
	return s.host.userActive != nil && s.host.userActive()
}

func interactionNotification(ev agentprovider.HookEvent) bool {
	if ev.Event != agentprovider.EventInteraction {
		return false
	}
	typ := strings.ToLower(ev.NotificationType)
	return typ == "" ||
		strings.Contains(typ, "permission") ||
		strings.Contains(typ, "elicitation") ||
		(typ == "auth_required" || typ == "login_required")
}

func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}

func truncatePrompt(prompt string) string {
	const max = 80
	prompt = strings.TrimSpace(prompt)
	if len(prompt) <= max {
		return prompt
	}
	return prompt[:max] + "..."
}
