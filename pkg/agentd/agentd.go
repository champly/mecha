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
	// Logger receives agentd logs. Nil selects the default determined by
	// LogStdout.
	Logger *slog.Logger
	// LogStdout routes agentd logs to stdout without creating a log file.
	// serve mode sets it; the default (run mode) writes process logs to the
	// workspace log file.
	LogStdout bool
	// Context bounds startup and the lifetime of the Core connection. Nil
	// means context.Background(). cmd/agentd passes a signal-aware context so
	// SIGTERM during Register/TaskChannel aborts startup promptly.
	Context context.Context
}

// startupTimeout bounds the Register and TaskChannel setup RPCs. It does not
// bound the lifetime of the task stream itself.
const startupTimeout = 10 * time.Second

// agentdCloseBudget is the expected worst-case Close duration:
// 2s agent SIGTERM grace + 1s post-SIGKILL wait + 1s webhook Shutdown, plus a
// small gRPC-conn/scheduling tail. Core's minimum ShutdownTimeout is 5s, which
// leaves this budget a 1s reserve so Core never SIGKILLs agentd before the
// independent-session agent gets TERM and is reaped.
const agentdCloseBudget = defaultAgentStopGrace + defaultAgentKillWait + webhookCloseTimeout

// Agentd manages a single agent process and communicates with Core via gRPC.
type Agentd struct {
	opts    Options
	logger  *slog.Logger
	client  api.CoreClient
	conn    *grpc.ClientConn
	webhook *WebhookServer
	hookCh  chan agentprovider.HookEvent

	ptmx     *os.File
	cmd      *exec.Cmd
	logFile  *os.File
	agentPID int
	taskCh   chan taskResult
	stop     chan struct{}
	ready    chan struct{}

	// agentDone closes after waitAgent has reaped the agent leader. It lets
	// Close bound its wait without ever calling cmd.Wait twice.
	agentDone chan struct{}

	mu           sync.Mutex // guards ptmx, cmd, hasTask and streamCancel
	streamCancel context.CancelFunc
	hasTask      bool
	agentType    string
	// ptyWrite is a test seam for observing task PTY writes. It must be set
	// before tasks start and is read-only afterwards; nil writes directly to
	// the PTY.
	ptyWrite   func(*os.File, string) (int, error)
	lastOutput atomic.Int64 // unix nano of last agent output, for TUI readiness
	closeOnce  sync.Once
	stopOnce   sync.Once
	closed     chan struct{} // closed when Close finishes
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
	logger := opts.Logger
	if logger == nil {
		logger = defaultLogger()
	}
	return &Agentd{
		opts:      opts,
		logger:    logger,
		stop:      make(chan struct{}),
		ready:     make(chan struct{}),
		taskCh:    make(chan taskResult, 1),
		hookCh:    make(chan agentprovider.HookEvent, 32),
		agentDone: make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

// defaultLogger writes structured logs to stdout, matching Core's default.
func defaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// initLogging selects the logger used by Start. An injected logger always wins.
// serve mode (LogStdout) logs to stdout without creating a file. Otherwise logs
// are appended to the workspace log file, matching run mode. On failure the
// agentd keeps its default logger and slog stays on stderr; Start continues.
func (a *Agentd) initLogging() bool {
	if a.opts.Logger != nil {
		a.logger = a.opts.Logger
		return true
	}
	if a.opts.LogStdout {
		a.logger = defaultLogger()
		return true
	}

	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	logger, f, err := config.NewFileLogger(wd)
	if err != nil {
		return false
	}
	a.logFile = f
	a.logger = logger
	return true
}

// Start starts the webhook server, registers with Core, and launches the agent.
func (a *Agentd) Start() error {
	if a.initLogging() {
		slog.SetDefault(a.logger)
	}

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

	registerCtx, cancel := context.WithTimeout(a.ctx(), startupTimeout)
	cfg, err := a.client.Register(registerCtx, &api.RegisterRequest{Id: a.opts.ID})
	cancel()
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

	// A signal may arrive after the RPCs but before the agent starts.
	if err := a.baseContext().Err(); err != nil {
		a.Close()
		return fmt.Errorf("agentd: startup canceled: %w", err)
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

// Stop gracefully terminates the agent and releases all resources. It is
// idempotent and safe to call from a signal handler goroutine.
func (a *Agentd) Stop() {
	a.signalStop()
	a.Close()
}

// baseContext returns the injected lifetime context or Background.
func (a *Agentd) baseContext() context.Context {
	if a.opts.Context != nil {
		return a.opts.Context
	}
	return context.Background()
}

// ctx returns a context with the agentd ID as gRPC metadata. It inherits
// cancellation from Options.Context.
func (a *Agentd) ctx() context.Context {
	return api.NewContextWithID(a.baseContext(), a.opts.ID)
}

// Wait blocks until the agent process exits and shutdown has finished. Waiting
// for Close (not just stop) ensures a SIGTERM handler cannot let the process
// exit while agent children are still being terminated.
func (a *Agentd) Wait() {
	<-a.stop
	<-a.closed
}

// Close releases the agent process group first, then the webhook server, and
// finally the Core connection. Agent cleanup must happen before anything that
// can block (webhook Shutdown is bounded to 1s): the agent lives in its own
// session, so if Core's 5s pane budget expires before agentd reaps it, SIGKILL
// of agentd cannot reach the agent. Safe to call multiple times.
func (a *Agentd) Close() {
	a.closeOnce.Do(func() {
		// Close by itself must also unblock Wait; do not rely on Stop/supervise.
		a.signalStop()

		// The agent is a session/process-group leader (pty sets Setsid), so
		// signal the whole group: killing only the leader leaves descendants
		// such as shell wrappers and tool subprocesses behind. stopAgent is
		// bounded by 2s grace + 1s post-SIGKILL wait, keeping agentd inside
		// Core's minimum 5s pane budget even before webhook/conn teardown.
		a.stopAgent()

		// Cancel the long-lived TaskChannel stream before closing the
		// connection so Recv unblocks even if the transport is wedged.
		a.mu.Lock()
		cancelStream := a.streamCancel
		a.streamCancel = nil
		a.mu.Unlock()
		if cancelStream != nil {
			cancelStream()
		}

		// Webhook Shutdown can wait for in-flight handlers, so it runs only
		// after the agent has been reaped and is bounded to 1s.
		if a.webhook != nil {
			if err := a.webhook.Close(); err != nil {
				slog.Warn("agentd: close webhook", "id", a.opts.ID, "err", err)
			}
		}
		if a.conn != nil {
			if err := a.conn.Close(); err != nil {
				slog.Warn("agentd: close core connection", "id", a.opts.ID, "err", err)
			}
		}

		if a.logFile != nil {
			_ = a.logFile.Close()
		}

		close(a.closed)
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

// reportStatus calls ReportStatus with a deadline so a hung Core cannot block
// hookLoop.
func (a *Agentd) reportStatus(status string) {
	if a.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx(), 2*time.Second)
	defer cancel()
	if _, err := a.client.ReportStatus(ctx, &api.StatusRequest{
		Id:     a.opts.ID,
		Status: status,
	}); err != nil {
		slog.Warn("agentd: report status", "id", a.opts.ID, "status", status, "err", err)
	}
}
