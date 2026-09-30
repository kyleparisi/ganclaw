// Package codex runs a single long-lived `codex app-server` subprocess and
// drives it over JSON-RPC. Codex owns the ChatGPT login (in CODEX_HOME) and
// all conversation state; each ganclaw session maps to one codex thread.
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
}

type Provider struct {
	cfg  Config
	log  *slog.Logger
	proc *procexec.Proc
	rpc  *conn
	done chan struct{} // closed when the process exits

	mu     sync.Mutex
	turns  map[string]*turnState // active turn per thread ID
	loaded map[string]bool       // threads loaded in this process
	exit   error
	closed bool // Close was called; exit is expected
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
	log := logx.OrDiscard(cfg.Logger).With("provider", "codex")

	env := procexec.FilterEnv(cfg.Environ(), func(k string) bool { return k == "CODEX_HOME" })
	if cfg.CodexHome != "" {
		env = append(env, "CODEX_HOME="+cfg.CodexHome)
	}
	proc, err := cfg.Exec.Start(context.Background(), procexec.Cmd{
		Bin:    cfg.Bin,
		Args:   appServerArgs(cfg.Overrides),
		Env:    env,
		Stderr: logx.LineWriter(log, slog.LevelDebug, "codex stderr"),
	})
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	p := &Provider{
		cfg:    cfg,
		log:    log,
		proc:   proc,
		done:   make(chan struct{}),
		turns:  map[string]*turnState{},
		loaded: map[string]bool{},
	}
	p.rpc = newConn(proc.Stdin, p.handleNotification, p.handleRequest)
	go func() {
		_ = p.rpc.readLoop(proc.Stdout)
		err := proc.Wait()
		p.mu.Lock()
		p.exit = err
		expected := p.closed
		p.mu.Unlock()
		close(p.done)
		if expected {
			log.Info("codex app-server stopped")
		} else {
			log.Warn("codex app-server exited unexpectedly", "err", err)
		}
	}()

	var init initializeResponse
	if err := p.rpc.call(ctx, "initialize", initializeParams{
		ClientInfo: clientInfo{Name: "ganclaw", Title: "ganclaw", Version: cfg.ClientVersion},
	}, &init); err != nil {
		p.Close()
		return nil, fmt.Errorf("codex initialize: %w", err)
	}
	if err := p.rpc.notify("initialized", nil); err != nil {
		p.Close()
		return nil, err
	}
	log.Info("codex app-server ready", "user_agent", init.UserAgent)
	return p, nil
}

func (p *Provider) Name() string { return "codex" }

// Run sends one user turn and waits for it to finish.
func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	select {
	case <-p.done:
		return provider.Result{}, fmt.Errorf("%w: codex app-server exited: %v", provider.ErrUnavailable, p.exitErr())
	default:
	}
	start := time.Now()

	threadID, err := p.ensureThread(ctx, req)
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
	if err := p.rpc.call(ctx, "turn/start", turnStartParams{
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
		ierr := p.rpc.call(ictx, "turn/interrupt", turnInterruptParams{ThreadID: threadID, TurnID: started.Turn.ID}, nil)
		log.Info("turn cancelled", "interrupt_err", ierr)
		return provider.Result{Session: threadID}, ctx.Err()
	case <-p.done:
		return provider.Result{Session: threadID}, fmt.Errorf("%w: codex app-server exited mid-turn: %v", provider.ErrUnavailable, p.exitErr())
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
func (p *Provider) ensureThread(ctx context.Context, req provider.Request) (string, error) {
	params := threadParams{
		Cwd:            req.Cwd,
		Model:          firstNonEmpty(req.Settings.CodexModel, p.cfg.Model),
		ApprovalPolicy: "never",
		Sandbox:        firstNonEmpty(req.Settings.CodexSandbox, p.cfg.Sandbox),
	}
	var resp threadResponse
	if req.Session == "" {
		params.DeveloperInstructions = req.Instructions
		params.ServiceName = "ganclaw"
		if err := p.rpc.call(ctx, "thread/start", params, &resp); err != nil {
			return "", p.wrapCallErr("thread/start", err)
		}
		p.log.Info("thread started", "thread", resp.Thread.ID, "model", resp.Model, "cwd", req.Cwd)
	} else {
		p.mu.Lock()
		loaded := p.loaded[req.Session]
		p.mu.Unlock()
		if loaded {
			return req.Session, nil
		}
		params.ThreadID = req.Session
		params.ExcludeTurns = true
		if err := p.rpc.call(ctx, "thread/resume", params, &resp); err != nil {
			return "", p.wrapCallErr("thread/resume", err)
		}
		p.log.Info("thread resumed", "thread", resp.Thread.ID, "model", resp.Model)
	}
	p.mu.Lock()
	p.loaded[resp.Thread.ID] = true
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

func (p *Provider) exitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
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

// Close stops the app-server, killing it if it doesn't exit promptly.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.proc.Stdin.Close()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.proc.Kill()
		<-p.done
	}
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
