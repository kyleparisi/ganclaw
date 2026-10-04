// Package codex runs a long-lived `codex app-server` subprocess and drives
// it over JSON-RPC. Codex owns the ChatGPT login (in CODEX_HOME) and all
// conversation state; each ganclaw session maps to one codex thread.
//
// The app-server keeps every thread it has loaded in memory, each with its
// own set of MCP server processes, and turn/interrupt leaves the turn's
// shell commands running. So the provider restarts the app-server whenever
// no turn is running and it has been idle, has too many threads loaded, or
// had a turn cancelled. Threads are persisted, so they resume afterwards.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

type Config struct {
	// Bin is the codex executable. Defaults to "codex" on PATH.
	Bin string
	// CodexHome holds codex's login and thread state. Run
	// `CODEX_HOME=<dir> codex login` once before first use.
	CodexHome string
	// Model overrides codex's default model. Optional.
	Model string
	// Sandbox is read-only, workspace-write (default) or danger-full-access.
	Sandbox string
	// ClientVersion is reported to the server in initialize.
	ClientVersion string
	// Overrides are codex config values passed as `-c key=value`, e.g.
	// `web_search="live"` or MCP server definitions. They are visible in
	// the process list, so never put secrets in them.
	Overrides []string
	// Environ returns the app-server's base environment. Defaults to
	// os.Environ. Everything in it is visible to agent shell commands.
	Environ func() []string
	// Exec starts the app-server. Zero value uses real processes.
	Exec procexec.Exec
	// Logger receives provider events and app-server stderr (at debug).
	Logger *slog.Logger
	// MaxLoadedThreads is how many threads may stay loaded before the
	// app-server is restarted at the next idle moment. Default 4.
	MaxLoadedThreads int
	// IdleRestart restarts the app-server after this long without a turn,
	// unloading every thread. Default 10 minutes.
	IdleRestart time.Duration
}

type Provider struct {
	cfg Config
	log *slog.Logger

	// gate is held shared by each Run and exclusively while the app-server
	// is replaced. Restarts only ever TryLock it, so they never cut a turn
	// short or make new turns wait.
	gate sync.RWMutex

	mu     sync.Mutex
	srv    *server
	turns  map[string]*turnState // active turn per thread ID
	stale  bool                  // a turn was cancelled; its commands may still run
	idle   *time.Timer
	closed bool // Close was called
}

// server is one app-server process. Its loaded, exit and stopped fields
// are guarded by Provider.mu.
type server struct {
	proc    *procexec.Proc
	rpc     *conn
	done    chan struct{}   // closed when the process exits
	loaded  map[string]bool // threads loaded in this process
	exit    error
	stopped bool // stop was called; exit is expected
}

func (s *server) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// turnState collects notifications for the one active turn on a thread.
type turnState struct {
	onDelta func(string)
	phase   map[string]string // itemId -> phase
	final   string            // last final_answer message
	last    string            // last agent message of any phase
	done    chan turn
}

// New starts the app-server and completes the initialize handshake. ctx
// bounds the handshake only; the process runs until Close.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Bin == "" {
		cfg.Bin = "codex"
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = "workspace-write"
	}
	if cfg.Exec.Start == nil {
		cfg.Exec = procexec.OS
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.MaxLoadedThreads <= 0 {
		cfg.MaxLoadedThreads = 4
	}
	if cfg.IdleRestart <= 0 {
		cfg.IdleRestart = 10 * time.Minute
	}
	p := &Provider{
		cfg:   cfg,
		log:   logx.OrDiscard(cfg.Logger).With("provider", "codex"),
		turns: map[string]*turnState{},
	}
	srv, err := p.start(ctx)
	if err != nil {
		return nil, err
	}
	p.srv = srv
	return p, nil
}

// start launches an app-server and completes the initialize handshake.
func (p *Provider) start(ctx context.Context) (*server, error) {
	log := p.log
	env := procexec.FilterEnv(p.cfg.Environ(), func(k string) bool { return k == "CODEX_HOME" })
	if p.cfg.CodexHome != "" {
		env = append(env, "CODEX_HOME="+p.cfg.CodexHome)
	}
	proc, err := p.cfg.Exec.Start(context.Background(), procexec.Cmd{
		Bin:    p.cfg.Bin,
		Args:   appServerArgs(p.cfg.Overrides),
		Env:    env,
		Stderr: logx.LineWriter(log, slog.LevelDebug, "codex stderr"),
	})
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	srv := &server{
		proc:   proc,
		done:   make(chan struct{}),
		loaded: map[string]bool{},
	}
	srv.rpc = newConn(proc.Stdin, p.handleNotification, p.handleRequest)
	go func() {
		_ = srv.rpc.readLoop(proc.Stdout)
		err := proc.Wait()
		p.mu.Lock()
		srv.exit = err
		expected := srv.stopped
		p.mu.Unlock()
		close(srv.done)
		if expected {
			log.Info("codex app-server stopped")
		} else {
			log.Warn("codex app-server exited unexpectedly", "err", err)
		}
	}()

	var init initializeResponse
	if err := srv.rpc.call(ctx, "initialize", initializeParams{
		ClientInfo: clientInfo{Name: "ganclaw", Title: "ganclaw", Version: p.cfg.ClientVersion},
	}, &init); err != nil {
		p.stop(srv)
		return nil, fmt.Errorf("codex initialize: %w", err)
	}
	if err := srv.rpc.notify("initialized", nil); err != nil {
		p.stop(srv)
		return nil, err
	}
	log.Info("codex app-server ready", "user_agent", init.UserAgent)
	return srv, nil
}

func (p *Provider) Name() string { return "codex" }

// Run sends one user turn and waits for it to finish.
func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	p.gate.RLock()
	res, err := p.run(ctx, p.server(), req)
	p.gate.RUnlock()
	p.afterRun()
	return res, err
}

func (p *Provider) run(ctx context.Context, srv *server, req provider.Request) (provider.Result, error) {
	if srv.exited() {
		return provider.Result{}, fmt.Errorf("%w: codex app-server exited: %v", provider.ErrUnavailable, p.exitErr(srv))
	}
	start := time.Now()

	threadID, err := p.ensureThread(ctx, srv, req)
	if err != nil {
		return provider.Result{}, err
	}
	log := p.log.With("thread", threadID)

	ts := &turnState{onDelta: req.OnDelta, phase: map[string]string{}, done: make(chan turn, 1)}
	p.mu.Lock()
	if p.turns[threadID] != nil {
		p.mu.Unlock()
		return provider.Result{}, fmt.Errorf("codex thread %s already has an active turn", threadID)
	}
	p.turns[threadID] = ts
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.turns, threadID)
		p.mu.Unlock()
	}()

	input := []userInput{{Type: "text", Text: req.Prompt}}
	for _, a := range req.Attachments {
		if a.Kind == provider.KindImage {
			input = append(input, userInput{Type: "localImage", Path: a.Path})
		}
	}
	var started turnStartResponse
	if err := srv.rpc.call(ctx, "turn/start", turnStartParams{
		ThreadID: threadID,
		Input:    input,
	}, &started); err != nil {
		return provider.Result{Session: threadID}, p.wrapCallErr("turn/start", err)
	}
	log = log.With("turn", started.Turn.ID)
	log.Debug("turn started", "prompt_len", len(req.Prompt))

	var t turn
	select {
	case t = <-ts.done:
	case <-ctx.Done():
		ictx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ierr := srv.rpc.call(ictx, "turn/interrupt", turnInterruptParams{ThreadID: threadID, TurnID: started.Turn.ID}, nil)
		log.Info("turn cancelled", "interrupt_err", ierr)
		// The interrupt doesn't stop commands the turn started.
		p.mu.Lock()
		p.stale = true
		p.mu.Unlock()
		return provider.Result{Session: threadID}, ctx.Err()
	case <-srv.done:
		return provider.Result{Session: threadID}, fmt.Errorf("%w: codex app-server exited mid-turn: %v", provider.ErrUnavailable, p.exitErr(srv))
	}

	switch t.Status {
	case "completed":
	case "failed":
		terr := newTurnError(t.Error)
		if limitCodes[terr.Code] {
			terr.Retry = p.retryAt(ctx)
		}
		log.Warn("turn failed", "code", terr.Code, "err", terr.Message, "retry_at", terr.Retry, "duration", time.Since(start))
		return provider.Result{Session: threadID}, terr
	default:
		return provider.Result{Session: threadID}, fmt.Errorf("codex turn ended with status %q", t.Status)
	}

	text := ts.final
	if text == "" {
		text = ts.last
	}
	text = strings.TrimSpace(text)
	log.Info("turn completed", "reply_len", len(text), "duration", time.Since(start))
	return provider.Result{Session: threadID, Text: text}, nil
}

// ensureThread starts a new thread or resumes an existing one into this
// process, returning its ID.
func (p *Provider) ensureThread(ctx context.Context, srv *server, req provider.Request) (string, error) {
	// Instructions go with resumes too, so changes to an agent's files
	// reach existing conversations.
	params := threadParams{
		Cwd:                   req.Cwd,
		Model:                 firstNonEmpty(req.Settings.CodexModel, p.cfg.Model),
		ApprovalPolicy:        "never",
		Sandbox:               firstNonEmpty(req.Settings.CodexSandbox, p.cfg.Sandbox),
		DeveloperInstructions: req.Instructions,
	}
	var resp threadResponse
	if req.Session == "" {
		params.ServiceName = "ganclaw"
		if err := srv.rpc.call(ctx, "thread/start", params, &resp); err != nil {
			return "", p.wrapCallErr("thread/start", err)
		}
		p.log.Info("thread started", "thread", resp.Thread.ID, "model", resp.Model, "cwd", req.Cwd)
	} else {
		p.mu.Lock()
		loaded := srv.loaded[req.Session]
		p.mu.Unlock()
		if loaded {
			return req.Session, nil
		}
		params.ThreadID = req.Session
		params.ExcludeTurns = true
		if err := srv.rpc.call(ctx, "thread/resume", params, &resp); err != nil {
			return "", p.wrapCallErr("thread/resume", err)
		}
		p.log.Info("thread resumed", "thread", resp.Thread.ID, "model", resp.Model)
	}
	p.mu.Lock()
	srv.loaded[resp.Thread.ID] = true
	p.mu.Unlock()
	return resp.Thread.ID, nil
}

func (p *Provider) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/started", "item/completed":
		var n itemNotification
		if json.Unmarshal(params, &n) != nil || n.Item.Type != "agentMessage" {
			return
		}
		ts := p.turn(n.ThreadID)
		if ts == nil {
			return
		}
		ts.phase[n.Item.ID] = n.Item.Phase
		if method == "item/completed" {
			ts.last = n.Item.Text
			if n.Item.Phase == "final_answer" {
				ts.final = n.Item.Text
			}
		}
	case "item/agentMessage/delta":
		var n agentMessageDelta
		if json.Unmarshal(params, &n) != nil {
			return
		}
		if ts := p.turn(n.ThreadID); ts != nil && ts.onDelta != nil && ts.phase[n.ItemID] != "commentary" {
			ts.onDelta(n.Delta)
		}
	case "turn/completed":
		var n turnCompleted
		if json.Unmarshal(params, &n) != nil {
			return
		}
		if ts := p.turn(n.ThreadID); ts != nil {
			select {
			case ts.done <- n.Turn:
			default:
			}
		}
	case "error":
		var n errorNotification
		if json.Unmarshal(params, &n) == nil {
			p.log.Warn("codex error", "thread", n.ThreadID, "code", n.Error.Code(), "err", n.Error.Message, "will_retry", n.WillRetry)
		}
	}
}

// handleRequest answers server-initiated requests. Threads run with
// approvalPolicy "never", so approval prompts are unexpected; decline them
// rather than hang the turn.
func (p *Provider) handleRequest(method string, params json.RawMessage) (any, error) {
	p.log.Warn("unexpected server request, declining", "method", method)
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "decline"}, nil
	case "execCommandApproval", "applyPatchApproval":
		return map[string]string{"decision": "denied"}, nil
	}
	return nil, fmt.Errorf("ganclaw does not handle %s", method)
}

func (p *Provider) turn(threadID string) *turnState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turns[threadID]
}

func (p *Provider) server() *server {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.srv
}

func (p *Provider) exitErr(srv *server) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return srv.exit
}

func (p *Provider) wrapCallErr(method string, err error) error {
	var re *rpcError
	if errors.As(err, &re) {
		return fmt.Errorf("codex %s: %w", method, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Transport failure: the process is gone or unwritable.
	return fmt.Errorf("%w: codex %s: %v", provider.ErrUnavailable, method, err)
}

// afterRun schedules the idle restart and restarts right away if the
// app-server needs it and no other turn is running.
func (p *Provider) afterRun() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(p.cfg.IdleRestart, func() { p.restart("idle") })
	var reason string
	switch {
	case p.srv.exited():
		reason = "app-server exited"
	case p.stale:
		reason = "turn cancelled"
	case len(p.srv.loaded) > p.cfg.MaxLoadedThreads:
		reason = "too many loaded threads"
	}
	p.mu.Unlock()
	if reason != "" {
		p.restart(reason)
	}
}

// restart replaces the app-server if no turn is running. Otherwise it does
// nothing: the running turns call afterRun when they finish.
func (p *Provider) restart(reason string) {
	if !p.gate.TryLock() {
		return
	}
	defer p.gate.Unlock()

	p.mu.Lock()
	old := p.srv
	loaded := len(old.loaded)
	skip := p.closed || (reason == "idle" && loaded == 0 && !old.exited())
	p.mu.Unlock()
	if skip {
		return
	}

	p.log.Info("restarting codex app-server", "reason", reason, "loaded_threads", loaded)
	p.stop(old)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv, err := p.start(ctx)
	if err != nil {
		// Keep a dead server so turns fail over; the next Run retries.
		p.log.Warn("codex app-server restart failed", "err", err)
		srv = &server{done: make(chan struct{}), loaded: map[string]bool{}, exit: err}
		close(srv.done)
	}

	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.srv = srv
		p.stale = false
	}
	p.mu.Unlock()
	if closed {
		p.stop(srv)
	}
}

// stop closes the app-server's stdin, which makes codex exit and kill its
// MCP servers and running commands, and kills it if it doesn't exit
// promptly.
func (p *Provider) stop(srv *server) {
	if srv.proc == nil {
		return
	}
	p.mu.Lock()
	srv.stopped = true
	p.mu.Unlock()
	_ = srv.proc.Stdin.Close()
	select {
	case <-srv.done:
	case <-time.After(5 * time.Second):
		_ = srv.proc.Kill()
		<-srv.done
	}
}

// Close stops the app-server.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	if p.idle != nil {
		p.idle.Stop()
	}
	srv := p.srv
	p.mu.Unlock()
	p.stop(srv)
	return nil
}

// TurnError is a failed codex turn.
type TurnError struct {
	Code    string
	Message string
	// Retry is when the limit behind a usage/rate-limit failure resets.
	Retry time.Time
}

// RetryAt implements provider.RetryAter.
func (e *TurnError) RetryAt() time.Time { return e.Retry }

func newTurnError(e *turnError) *TurnError {
	if e == nil {
		return &TurnError{Message: "turn failed without error details"}
	}
	msg := e.Message
	if e.AdditionalDetails != "" {
		msg += ": " + e.AdditionalDetails
	}
	return &TurnError{Code: e.Code(), Message: msg}
}

func (e *TurnError) Error() string {
	if e.Code == "" {
		return "codex turn failed: " + e.Message
	}
	return fmt.Sprintf("codex turn failed (%s): %s", e.Code, e.Message)
}

// Is reports whether the failure should trigger a fallback provider.
func (e *TurnError) Is(target error) bool {
	if target != provider.ErrUnavailable {
		return false
	}
	switch e.Code {
	case "usageLimitExceeded", "rateLimitExceeded", "serverOverloaded", "internalServerError",
		"unauthorized", "httpConnectionFailed", "responseStreamConnectionFailed",
		"responseStreamDisconnected", "responseTooManyFailedAttempts":
		return true
	}
	return false
}

func appServerArgs(overrides []string) []string {
	args := []string{"app-server"}
	for _, o := range overrides {
		args = append(args, "-c", o)
	}
	return args
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
