package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentprovider "github.com/champly/mecha/pkg/agent/provider"
	"github.com/champly/mecha/pkg/api"
	"github.com/champly/mecha/pkg/config"
)

// Options configures an agentd instance.
type Options struct {
	ID       string
	CoreAddr string // "127.0.0.1:PORT"
}

// Agentd manages a single agent process and communicates with Core via gRPC.
type Agentd struct {
	opts    Options
	client  api.CoreClient
	conn    *grpc.ClientConn
	webhook *WebhookServer
	hookCh  chan agentprovider.HookEvent

	ptmx    *os.File
	cmd     *exec.Cmd
	taskCh  chan taskResult
	stop    chan struct{}
	ready   chan struct{}
	logFile *os.File

	mu         sync.Mutex // guards ptmx, cmd and hasTask
	hasTask    bool
	agentType  string
	lastOutput atomic.Int64 // unix nano of last agent output, for TUI readiness
	closeOnce  sync.Once
	stopOnce   sync.Once
}

// signalStop closes stop exactly once, so a dead Core cannot orphan this
// agentd.
func (a *Agentd) signalStop() {
	a.stopOnce.Do(func() {
		close(a.stop)
	})
}

// New creates a new Agentd instance.
func New(opts Options) *Agentd {
	return &Agentd{
		opts:   opts,
		stop:   make(chan struct{}),
		ready:  make(chan struct{}),
		taskCh: make(chan taskResult, 1),
		hookCh: make(chan agentprovider.HookEvent, 32),
	}
}

// Start starts the webhook server, registers with Core, and launches the agent.
func (a *Agentd) Start() error {
	a.initLogging()

	wh, err := NewWebhookServer(a.hookCh)
	if err != nil {
		return fmt.Errorf("agentd: %w", err)
	}
	a.webhook = wh

	conn, err := grpc.NewClient(a.opts.CoreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		a.Close()
		return fmt.Errorf("agentd: dial: %w", err)
	}
	a.conn = conn
	a.client = api.NewCoreClient(conn)

	cfg, err := a.client.Register(a.ctx(), &api.RegisterRequest{Id: a.opts.ID})
	if err != nil {
		a.Close()
		return fmt.Errorf("agentd: register: %w", err)
	}

	// Open the task channel before launching the agent so Core can dispatch
	// tasks as soon as the instance reports ready.
	if err := a.connectTaskChannel(); err != nil {
		a.Close()
		return err
	}

	if err := a.startAgent(cfg, wh.Addr()); err != nil {
		a.Close()
		return fmt.Errorf("agentd: start agent: %w", err)
	}

	go a.hookLoop()
	go a.supervise()

	// Report readiness after the PTY-backed agent process has launched.
	// Codex's TUI only emits SessionStart after the first submitted prompt, so
	// waiting for that hook would deadlock startup on an idle coordinator.
	a.reportStatus(api.StatusStarted)

	return nil
}

// supervise waits for the agent to exit and performs orderly shutdown.
func (a *Agentd) supervise() {
	<-a.stop
	a.reportStatus(api.StatusExited)
	a.Close()
}

// ctx returns a context with the agentd ID as gRPC metadata.
func (a *Agentd) ctx() context.Context {
	return api.NewContextWithID(context.Background(), a.opts.ID)
}

// Wait blocks until the agent process exits.
func (a *Agentd) Wait() {
	<-a.stop
}

// Close releases the Core connection, webhook server, log file, PTY, and
// agent process. Safe to call multiple times.
func (a *Agentd) Close() {
	a.closeOnce.Do(func() {
		if a.conn != nil {
			a.conn.Close()
		}
		if a.webhook != nil {
			a.webhook.Close()
		}
		if a.logFile != nil {
			a.logFile.Close()
		}

		// Closing the PTY hangs up the agent; Kill is the fallback.
		a.mu.Lock()
		ptmx := a.ptmx
		a.ptmx = nil
		cmd := a.cmd
		a.cmd = nil
		a.mu.Unlock()

		if ptmx != nil {
			_ = ptmx.Close()
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
}

// hookLoop dispatches webhook events until shutdown. The channel is never
// closed, so a handler can never panic on a send.
func (a *Agentd) hookLoop() {
	for {
		select {
		case <-a.stop:
			return
		case ev := <-a.hookCh:
			a.handleHook(ev)
		}
	}
}

// handleHook handles a single webhook event from the agent process.
func (a *Agentd) handleHook(ev agentprovider.HookEvent) {
	switch ev.Event {
	case agentprovider.EventSessionStart:
		a.reportStatus(api.StatusStarted)

	case agentprovider.EventStop:
		if r, ok := a.takeTaskResult(taskResult{success: true, result: ev.Output}); ok {
			a.taskCh <- r
		}

	case agentprovider.EventStopFailure:
		if r, ok := a.takeTaskResult(taskResult{result: ev.Error}); ok {
			a.taskCh <- r
		}
	}
}

// takeTaskResult claims the task slot, reporting false when no task is waiting.
func (a *Agentd) takeTaskResult(r taskResult) (taskResult, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.hasTask {
		return taskResult{}, false
	}
	a.hasTask = false
	return r, true
}

// initLogging points the default slog handler at the shared log file; on
// failure slog stays on stderr.
func (a *Agentd) initLogging() {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	logger, f, err := config.NewFileLogger(wd)
	if err != nil {
		return
	}
	a.logFile = f
	slog.SetDefault(logger)
}

// reportStatus calls ReportStatus with a deadline so a hung Core cannot block
// hookLoop.
func (a *Agentd) reportStatus(status string) {
	ctx, cancel := context.WithTimeout(a.ctx(), 2*time.Second)
	defer cancel()
	if _, err := a.client.ReportStatus(ctx, &api.StatusRequest{
		Id:     a.opts.ID,
		Status: status,
	}); err != nil {
		slog.Warn("agentd: report status", "id", a.opts.ID, "status", status, "err", err)
	}
}
