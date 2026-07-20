package agentd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/champly/mecha/pkg/agent/types"
	"github.com/champly/mecha/pkg/api"
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
	hookCh  chan types.HookEvent

	ptmx   *os.File
	taskCh chan taskResult
	stop   chan struct{}
	ready  chan struct{}

	mu         sync.Mutex // guards ptmx and hasTask
	hasTask    bool
	lastOutput atomic.Int64 // unix nano of last agent output, for TUI readiness
	closeOnce  sync.Once
}

// New creates a new Agentd instance.
func New(opts Options) *Agentd {
	return &Agentd{
		opts:   opts,
		stop:   make(chan struct{}),
		ready:  make(chan struct{}),
		taskCh: make(chan taskResult, 1),
		hookCh: make(chan types.HookEvent, 8),
	}
}

// Start starts the webhook server, connects to Core via gRPC, registers, and launches the agent.
func (a *Agentd) Start() error {
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
	// tasks as soon as the instance reports ready (SessionStart).
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

	return nil
}

// supervise waits for the agent to exit and performs orderly shutdown.
func (a *Agentd) supervise() {
	<-a.stop
	a.reportStatus(api.StatusExited, "")
	a.Close()
	close(a.hookCh)
}

// ctx returns a context with the agentd ID as gRPC metadata.
func (a *Agentd) ctx() context.Context {
	return api.NewContextWithID(context.Background(), a.opts.ID)
}

// Wait blocks until the agent process exits.
func (a *Agentd) Wait() {
	<-a.stop
}

// Close releases resources held by agentd. It is safe to call multiple times.
func (a *Agentd) Close() {
	a.closeOnce.Do(func() {
		if a.conn != nil {
			a.conn.Close()
		}
		if a.webhook != nil {
			a.webhook.Close()
		}
	})
}

// hookLoop reads webhook events and dispatches them.
func (a *Agentd) hookLoop() {
	for ev := range a.hookCh {
		a.handleHook(ev)
	}
}

// handleHook handles a single webhook event from the agent process.
func (a *Agentd) handleHook(ev types.HookEvent) {
	switch ev.Event {
	case types.EventSessionStart:
		a.reportStatus(api.StatusStarted, "")

	case types.EventStop:
		a.mu.Lock()
		if a.hasTask {
			a.taskCh <- taskResult{success: true, result: ev.Output}
			a.hasTask = false
		}
		a.mu.Unlock()

	case types.EventStopFailure:
		a.mu.Lock()
		if a.hasTask {
			a.taskCh <- taskResult{result: ev.Error}
			a.hasTask = false
		}
		a.mu.Unlock()
	}
}

// reportStatus calls ReportStatus RPC.
func (a *Agentd) reportStatus(status, msg string) {
	_, _ = a.client.ReportStatus(a.ctx(), &api.StatusRequest{
		Id:     a.opts.ID,
		Status: status,
		Msg:    msg,
	})
}
