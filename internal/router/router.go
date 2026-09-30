// Package router runs agent turns for incoming chat messages, one at a time
// per chat, persisting each chat's provider sessions between turns.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/attachments"
	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// Message is one incoming chat message, with callbacks into its channel.
type Message struct {
	// ChatKey identifies the conversation, e.g. "telegram:<bot>:<chat>".
	ChatKey string
	Agent   string
	Text    string
	// Reply sends text back to the chat.
	Reply func(ctx context.Context, text string) error
	// Typing shows a "working" indicator. Optional.
	Typing func(ctx context.Context) error
	// Stream starts a streamed reply for an agent turn. Optional; without
	// it the final text is sent with Reply.
	Stream func(ctx context.Context) *ReplyStream
	// Fetch downloads the message's attachments into dir. Optional. It
	// runs in the chat's worker, so attachments stay in message order.
	Fetch func(ctx context.Context, dir string) ([]provider.Attachment, error)
	// Ctx, if set, cancels the turn when done (e.g. an API caller gave up).
	Ctx context.Context
	// OnDone, if set, is called once the message has been handled.
	OnDone func(TurnResult)
}

// TurnResult is the outcome of handling one message.
type TurnResult struct {
	Provider string // empty for commands and failures
	Text     string // the reply text, or the error message shown
	Err      error
}

// ReplyStream is a reply that is built up while the agent works.
type ReplyStream struct {
	// Append adds streamed text.
	Append func(text string)
	// Reset discards streamed text (a provider failed mid-reply and the
	// next one starts over).
	Reset func()
	// Finish replaces the streamed text with final and flushes it.
	Finish func(ctx context.Context, final string) error
}

// Command is a chat command, for channel menus such as Telegram's.
type Command struct {
	Name        string // without the leading slash
	Description string
}

// Commands lists the commands the router understands.
var Commands = []Command{
	{Name: "new", Description: "Start a new conversation"},
	{Name: "reset", Description: "Clear the conversation (same as /new)"},
	{Name: "stop", Description: "Stop the reply in progress"},
	{Name: "status", Description: "Show model availability and limits"},
	{Name: "help", Description: "List commands"},
}

// errStopped is the cancel cause for turns stopped with /stop.
var errStopped = errors.New("stopped by user")

type Config struct {
	Agents map[string]agent.Agent
	Store  *store.Store
	// Run executes a turn, typically (*provider.Chain).Run.
	Run func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error)
	// Status reports provider status for /status, typically
	// (*provider.Chain).Status. Optional.
	Status func(ctx context.Context) []provider.Status
	// Now defaults to time.Now; used to format /status.
	Now func() time.Time
	// Transcribe turns an audio attachment into text. Optional; without
	// it audio is passed to the agent as a file only.
	Transcribe func(ctx context.Context, path string) (string, error)

	TurnTimeout    time.Duration // default 20m
	TypingInterval time.Duration // default 4s
	IdleTimeout    time.Duration // idle per-chat worker exits after this; default 5m
	QueueSize      int           // pending messages per chat; default 16
	Logger         *slog.Logger
}

type Router struct {
	cfg Config
	log *slog.Logger
	ctx context.Context

	mu     sync.Mutex
	queues map[string]chan Message
	active map[string]context.CancelCauseFunc // running turn per chat
	wg     sync.WaitGroup
}

// New returns a router whose workers stop when ctx is cancelled.
func New(ctx context.Context, cfg Config) *Router {
	if cfg.TurnTimeout == 0 {
		cfg.TurnTimeout = 20 * time.Minute
	}
	if cfg.TypingInterval == 0 {
		cfg.TypingInterval = 4 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 16
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Router{
		cfg:    cfg,
		log:    logx.OrDiscard(cfg.Logger),
		ctx:    ctx,
		queues: map[string]chan Message{},
		active: map[string]context.CancelCauseFunc{},
	}
}

// Submit queues m for its chat. It returns false if the chat's queue is
// full or the router is shutting down.
func (r *Router) Submit(m Message) bool {
	if r.ctx.Err() != nil {
		return false
	}
	// /stop must not wait behind the turn it is meant to stop.
	if command(m.Text) == "stop" {
		r.stop(m)
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.queues[m.ChatKey]
	if !ok {
		q = make(chan Message, r.cfg.QueueSize)
		r.queues[m.ChatKey] = q
		r.wg.Add(1)
		go r.worker(m.ChatKey, q)
	}
	select {
	case q <- m:
		return true
	default:
		r.log.Warn("chat queue full, dropping message", "chat", m.ChatKey)
		return false
	}
}

// Wait blocks until all workers have exited (after ctx is cancelled).
func (r *Router) Wait() { r.wg.Wait() }

func (r *Router) worker(key string, q chan Message) {
	defer r.wg.Done()
	idle := time.NewTimer(r.cfg.IdleTimeout)
	defer idle.Stop()
	for {
		select {
		case m := <-q:
			r.process(m)
			idle.Reset(r.cfg.IdleTimeout)
		case <-r.ctx.Done():
			return
		case <-idle.C:
			// Exit only if nothing arrived; Submit holds mu while sending,
			// so no message can land in q after this check.
			r.mu.Lock()
			if len(q) == 0 {
				delete(r.queues, key)
				r.mu.Unlock()
				return
			}
			r.mu.Unlock()
			idle.Reset(r.cfg.IdleTimeout)
		}
	}
}

// stop cancels the chat's running turn, if any.
func (r *Router) stop(m Message) {
	r.mu.Lock()
	cancel := r.active[m.ChatKey]
	r.mu.Unlock()
	if cancel == nil {
		go func() {
			ctx, done := context.WithTimeout(r.ctx, 30*time.Second)
			defer done()
			if err := m.Reply(ctx, "Nothing is running."); err != nil {
				r.log.Error("reply failed", "chat", m.ChatKey, "err", err)
			}
		}()
		return
	}
	r.log.Info("turn stop requested", "chat", m.ChatKey)
	cancel(errStopped)
}

// command returns the command name if text is a slash command
// ("/status" or "/status@SomeBot"), else "".
func command(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \n") {
		return ""
	}
	name, _, _ := strings.Cut(text[1:], "@")
	return strings.ToLower(name)
}

// HelpText describes the commands.
func HelpText() string {
	var b strings.Builder
	b.WriteString("Send a message to talk to the agent. Commands:")
	for _, c := range Commands {
		fmt.Fprintf(&b, "\n/%s — %s", c.Name, c.Description)
	}
	return b.String()
}

func (r *Router) process(m Message) {
	log := r.log.With("chat", m.ChatKey, "agent", m.Agent)
	tctx, cancelTimeout := context.WithTimeout(r.ctx, r.cfg.TurnTimeout)
	defer cancelTimeout()
	ctx, cancel := context.WithCancelCause(tctx)
	defer cancel(nil)
	if m.Ctx != nil {
		stop := context.AfterFunc(m.Ctx, func() { cancel(context.Cause(m.Ctx)) })
		defer stop()
	}

	var result TurnResult
	if m.OnDone != nil {
		defer func() { m.OnDone(result) }()
	}

	reply := func(text string) {
		result.Text = text
		// Reply even if the turn timed out, but not after shutdown.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(r.ctx), 30*time.Second)
		defer rcancel()
		if r.ctx.Err() != nil {
			return
		}
		if err := m.Reply(rctx, text); err != nil {
			log.Error("reply failed", "err", err)
		}
	}

	cmd := command(m.Text)
	if m.Fetch != nil {
		cmd = "" // a caption on a file is a message, not a command
	}
	switch cmd {
	case "new", "reset":
		if err := r.cfg.Store.ResetSessions(ctx, m.ChatKey); err != nil {
			log.Error("reset sessions", "err", err)
			reply("Couldn't reset the conversation: " + err.Error())
			return
		}
		log.Info("conversation reset")
		reply("Started a new conversation.")
		return
	case "help", "start":
		reply(HelpText())
		return
	case "status":
		if r.cfg.Status == nil {
			reply("Status isn't available.")
			return
		}
		reply(provider.FormatStatus(r.cfg.Status(ctx), r.cfg.Now()))
		return
	}

	a, ok := r.cfg.Agents[m.Agent]
	if !ok {
		log.Error("unknown agent")
		reply("This bot isn't connected to an agent.")
		return
	}
	sessions, err := r.cfg.Store.Sessions(ctx, m.ChatKey)
	if err != nil {
		log.Error("load sessions", "err", err)
		reply("Couldn't load the conversation: " + err.Error())
		return
	}
	instructions, err := a.Instructions()
	if err != nil {
		log.Error("load instructions", "err", err)
		reply("Couldn't load the agent's instructions: " + err.Error())
		return
	}

	r.mu.Lock()
	r.active[m.ChatKey] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.active, m.ChatKey)
		r.mu.Unlock()
	}()

	// Typing covers downloading and transcribing too.
	stopTyping := r.typing(ctx, m)
	defer stopTyping()

	var atts []provider.Attachment
	if m.Fetch != nil {
		if atts, err = m.Fetch(ctx, attachments.Dir(a.Workspace)); err != nil {
			stopTyping()
			log.Warn("fetch attachments", "err", err)
			reply("Couldn't get the attachment: " + truncate(err.Error(), 300))
			return
		}
		log.Info("attachments saved", "count", len(atts))
		r.transcribe(ctx, log, atts)
	}

	req := provider.Request{
		Cwd:          a.Workspace,
		Instructions: instructions,
		Prompt:       BuildPrompt(m.Text, atts),
		Attachments:  atts,
		Settings:     a.Settings,
	}
	var stream *ReplyStream
	if m.Stream != nil {
		stream = m.Stream(ctx)
		req.OnDelta = stream.Append
		req.OnReset = stream.Reset
		// Anything already shown is replaced by the final text (or error).
		reply = func(text string) {
			result.Text = text
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(r.ctx), 30*time.Second)
			defer rcancel()
			if r.ctx.Err() != nil {
				return
			}
			if err := stream.Finish(rctx, text); err != nil {
				log.Error("reply failed", "err", err)
			}
		}
	}

	start := time.Now()
	used, res, runErr := r.cfg.Run(ctx, sessions, req)
	stopTyping()

	// Save even on failure: providers may have created sessions.
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer scancel()
	if err := r.cfg.Store.SaveSessions(sctx, m.ChatKey, sessions); err != nil {
		log.Error("save sessions", "err", err)
	}

	if runErr != nil {
		log.Warn("turn failed", "err", runErr, "duration", time.Since(start))
		reply(r.userError(ctx, runErr))
		result.Err = runErr
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
			result.Err = errors.Join(runErr, cause)
		}
		return
	}
	log.Info("turn answered", "provider", used, "reply_len", len(res.Text), "duration", time.Since(start))
	text := res.Text
	if text == "" {
		text = "(no reply)"
	}
	reply(text)
	result.Provider = used
}

// transcribe fills in transcripts for audio attachments. Failures are
// recorded on the attachment rather than failing the turn.
func (r *Router) transcribe(ctx context.Context, log *slog.Logger, atts []provider.Attachment) {
	if r.cfg.Transcribe == nil {
		return
	}
	for i := range atts {
		if atts[i].Kind != provider.KindAudio {
			continue
		}
		text, err := r.cfg.Transcribe(ctx, atts[i].Path)
		if err != nil {
			log.Warn("transcription failed", "file", atts[i].Name, "err", err)
			atts[i].TranscriptErr = truncate(err.Error(), 200)
			continue
		}
		atts[i].Transcript = text
	}
}

// typing refreshes the typing indicator until the returned func is called.
// Calling stop more than once is fine.
func (r *Router) typing(ctx context.Context, m Message) (stop func()) {
	if m.Typing == nil {
		return func() {}
	}
	tctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(r.cfg.TypingInterval)
		defer t.Stop()
		for {
			_ = m.Typing(tctx)
			select {
			case <-tctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func (r *Router) userError(ctx context.Context, err error) string {
	switch {
	case errors.Is(context.Cause(ctx), errStopped):
		return "Stopped."
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("That took longer than %s and was stopped.", r.cfg.TurnTimeout)
	case errors.Is(err, provider.ErrUnavailable):
		return "All models are unavailable right now. " + truncate(err.Error(), 400)
	default:
		return "Something went wrong: " + truncate(err.Error(), 400)
	}
}

// BuildPrompt appends a description of saved attachments to the user's text.
func BuildPrompt(text string, atts []provider.Attachment) string {
	if len(atts) == 0 {
		return text
	}
	var b strings.Builder
	if strings.TrimSpace(text) != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	b.WriteString("The user attached these files (saved in your workspace):")
	for _, a := range atts {
		fmt.Fprintf(&b, "\n- %s: %s (%s", a.Kind, a.Path, a.Name)
		if a.MimeType != "" {
			b.WriteString(", " + a.MimeType)
		}
		fmt.Fprintf(&b, ", %s)", humanSize(a.Size))
		switch {
		case a.Transcript != "":
			fmt.Fprintf(&b, "\n  Transcript: %s", a.Transcript)
		case a.TranscriptErr != "":
			fmt.Fprintf(&b, "\n  (Transcription failed: %s)", a.TranscriptErr)
		}
	}
	return b.String()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
