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

	"golang.org/x/term"
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

	ptmx       *os.File
	cmd        *exec.Cmd
	logFile    *os.File
	agentPID   int
	stop       chan struct{}
	screen     ScreenParser
	input      *inputArbiter
	session    *sessionActor
	userActive atomic.Bool

	// agentDone closes after waitAgent has reaped the agent leader. It lets
	// Close bound its wait without ever calling cmd.Wait twice.
	agentDone chan struct{}

	mu           sync.Mutex // guards ptmx, cmd and streamCancel
	streamCancel context.CancelFunc
	closeOnce    sync.Once
	stopOnce     sync.Once
	closed       chan struct{} // closed when Close finishes

	statusCh       chan statusReport
	statusStop     chan struct{}
	statusOnce     sync.Once
	statusStopOnce sync.Once
	statusWG       sync.WaitGroup
}

type statusReport struct {
	status string
	reason string
	done   chan struct{}
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
		opts:       opts,
		logger:     logger,
		stop:       make(chan struct{}),
		hookCh:     make(chan agentprovider.HookEvent, 32),
		agentDone:  make(chan struct{}),
		closed:     make(chan struct{}),
		statusCh:   make(chan statusReport, 16),
		statusStop: make(chan struct{}),
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
	a.startStatusLoop()

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
	a.labelPane(cfg.RoleName)

	// A signal may arrive after the RPCs but before the agent starts.
	if err := a.baseContext().Err(); err != nil {
		a.Close()
		return fmt.Errorf("agentd: startup canceled: %w", err)
	}

	if err := a.startAgent(cfg, wh.Addr()); err != nil {
		a.Close()
		return fmt.Errorf("agentd: start agent: %w", err)
	}

	// The session only reports Started after the TUI is positively ready, so
	// opening the stream after launching the process cannot allow early writes.
	if err := a.connectTaskChannel(); err != nil {
		a.Close()
		return err
	}

	go a.hookLoop()
	go a.supervise()

	return nil
}

func paneLabel(role string) string {
	return "\x1b]0;" + role + "\x07"
}

// labelPane writes an OSC title to the pane's terminal. serve/headless runs
// skip it so escape sequences never leak into captured logs.
func (a *Agentd) labelPane(role string) {
	if a.opts.LogStdout || role == "" || !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, paneLabel(role))
}

// supervise waits for the agent to exit and performs orderly shutdown.
func (a *Agentd) supervise() {
	<-a.stop
	a.reportStatus(api.StatusExited, "")
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
		a.stopStatusLoop()

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
	if a.session == nil {
		slog.Warn("agentd: hook received before session started", "event", ev.Event)
		return
	}
	if !a.session.enqueueHook(ev) {
		slog.Warn("agentd: session event queue unavailable", "event", ev.Event)
	}
}

func (a *Agentd) startStatusLoop() {
	a.statusOnce.Do(func() {
		a.statusWG.Add(1)
		go func() {
			defer a.statusWG.Done()
			a.statusLoop()
		}()
	})
}

func (a *Agentd) stopStatusLoop() {
	a.statusStopOnce.Do(func() { close(a.statusStop) })
	a.statusWG.Wait()
}

func (a *Agentd) statusLoop() {
	for {
		select {
		case report := <-a.statusCh:
			a.sendStatus(report)
		case <-a.statusStop:
			for {
				select {
				case report := <-a.statusCh:
					a.sendStatus(report)
				default:
					return
				}
			}
		}
	}
}

func (a *Agentd) sendStatus(report statusReport) {
	defer func() {
		if report.done != nil {
			close(report.done)
		}
	}()
	if a.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx(), 2*time.Second)
	defer cancel()
	if _, err := a.client.ReportStatus(ctx, &api.StatusRequest{
		Id:     a.opts.ID,
		Status: report.status,
		Reason: report.reason,
	}); err != nil {
		slog.Warn("agentd: report status", "id", a.opts.ID, "status", report.status, "err", err)
	}
}

// reportStatus delivers lifecycle updates off the session actor. A startup
// failure waits briefly so Core can return its reason; started and exited are
// best effort because the final agent cleanup must stay inside Core's budget.
func (a *Agentd) reportStatus(status, reason string) {
	report := statusReport{status: status, reason: reason}
	if status == api.StatusFailed {
		report.done = make(chan struct{})
	}
	select {
	case a.statusCh <- report:
	case <-a.statusStop:
		return
	}
	if report.done != nil {
		select {
		case <-report.done:
		case <-time.After(3 * time.Second):
		}
	}
}
