// Package appchannel is the channel for ganclaw's native apps. Phones talk
// to it over HTTPS and receive live updates over a WebSocket. Unlike
// Telegram it keeps each thread's history, since the phone has nowhere
// else to load it from.
//
// There is one thread per agent, with the conversation key "app:<agent>".
// Scheduled jobs deliver into the same thread, tagged with the job name.
package appchannel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/attachments"
	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// MaxFileSize caps uploads and files agents send to the app.
const MaxFileSize = 50 << 20

// Channel serves the app API and delivers agent replies into app threads.
type Channel struct {
	// Push is nil unless APNs is configured. Delivery failures never fail a reply.
	Push   func(context.Context, store.AppPush, store.AppMessage) (bool, error)
	Agents map[string]agent.Agent
	Store  *store.Store
	// Submit queues a turn, typically (*router.Router).Submit.
	Submit func(router.Message) bool
	// FilesDir holds uploaded files and copies of files agents send.
	FilesDir string
	Logger   *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// StreamInterval is how often streamed text is pushed to clients;
	// default 150ms.
	StreamInterval time.Duration
	// PairTTL is how long a pairing code works; default 10m.
	PairTTL time.Duration

	pushSlots chan struct{}
	once      sync.Once
	log       *slog.Logger
	hub       *hub
	mu        sync.Mutex
	state     map[string]AgentState
	codes     map[string]pairCode
}

// AgentState is what an agent is doing, for the inbox.
type AgentState struct {
	Working bool      `json:"working"`
	Detail  string    `json:"detail,omitempty"`
	Since   time.Time `json:"since,omitzero"`
}

type pairCode struct {
	name    string
	expires time.Time
}

func (c *Channel) setup() {
	c.once.Do(func() {
		c.log = logx.OrDiscard(c.Logger).With("channel", "app")
		if c.Now == nil {
			c.Now = time.Now
		}
		if c.StreamInterval == 0 {
			c.StreamInterval = 150 * time.Millisecond
		}
		if c.PairTTL == 0 {
			c.PairTTL = 10 * time.Minute
		}
		c.pushSlots = make(chan struct{}, 16)
		c.hub = newHub()
		c.state = map[string]AgentState{}
		c.codes = map[string]pairCode{}
	})
}

// Start marks replies a previous run left streaming as interrupted. Call it
// once before serving.
func (c *Channel) Start(ctx context.Context) error {
	c.setup()
	n, err := c.Store.FailStreamingAppMessages(ctx)
	if err != nil {
		return fmt.Errorf("app channel: %w", err)
	}
	if n > 0 {
		c.log.Info("marked interrupted replies", "count", n)
	}
	return os.MkdirAll(c.FilesDir, 0o700)
}

// ChatKey is the conversation key of an agent's app thread. A job run in
// its own session uses gateway.JobKey instead.
func ChatKey(agentName string) string { return "app:" + agentName }

// Event is pushed to every connected client.
type Event struct {
	Type    string            `json:"type"` // message | delta | agent
	Message *store.AppMessage `json:"message,omitempty"`
	// Delta events carry the whole streamed text so far, so a client that
	// missed one loses nothing.
	ID     int64       `json:"id,omitempty"`
	Thread string      `json:"thread,omitempty"`
	Text   string      `json:"text,omitempty"`
	Agent  string      `json:"agent,omitempty"`
	State  *AgentState `json:"state,omitempty"`
}

func (c *Channel) publishMessage(m store.AppMessage) {
	c.hub.publish(Event{Type: "message", Message: &m})
}

func (c *Channel) setState(agentName string, s AgentState) {
	c.mu.Lock()
	prev := c.state[agentName]
	if prev.Working == s.Working && prev.Detail == s.Detail {
		c.mu.Unlock()
		return
	}
	if s.Working && prev.Working {
		s.Since = prev.Since
	}
	c.state[agentName] = s
	c.mu.Unlock()
	c.hub.publish(Event{Type: "agent", Agent: agentName, State: &s})
}

func (c *Channel) agentState(agentName string) AgentState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state[agentName]
}

// Post adds a message to an agent's thread with no agent turn, e.g. a
// plain `ganclaw send` to the app.
func (c *Channel) Post(ctx context.Context, agentName, role, text, job string) (store.AppMessage, error) {
	return c.PostMessage(ctx, store.AppMessage{Thread: agentName, Role: role, Text: text, Job: job})
}

// PostMessage adds m to its thread (m.Thread names the agent) and pushes it
// to connected phones.
func (c *Channel) PostMessage(ctx context.Context, m store.AppMessage) (store.AppMessage, error) {
	c.setup()
	if _, ok := c.Agents[m.Thread]; !ok {
		return store.AppMessage{}, fmt.Errorf("no agent named %q", m.Thread)
	}
	m, err := c.Store.AddAppMessage(ctx, m)
	if err != nil {
		return m, err
	}
	c.publishMessage(m)
	if m.Role == store.RoleAgent && m.Status != store.StatusStreaming {
		c.notify(m)
	}
	return m, nil
}

// Message builds a router message whose reply lands in agentName's thread.
// job tags the reply as a scheduled job's run ("" for none). It is used for
// messages from the phone and for API runs delivered to the app.
func (c *Channel) Message(agentName, text, job string) router.Message {
	c.setup()
	t := &turn{c: c, agent: agentName, job: job}
	return router.Message{
		ChatKey: ChatKey(agentName),
		Agent:   agentName,
		Text:    text,
		Reply:   t.reply,
		Typing: func(context.Context) error {
			c.setState(agentName, AgentState{Working: true, Detail: "Working", Since: c.Now()})
			return nil
		},
		Stream:   t.stream,
		SendFile: t.sendFile,
		OnDone: func(router.TurnResult) {
			t.notifyFinal()
			c.setState(agentName, AgentState{})
		},
	}
}

// turn tracks the agent message one turn writes to.
type turn struct {
	c        *Channel
	agent    string
	job      string
	hidden   bool
	notified bool

	mu  sync.Mutex
	msg *store.AppMessage
}

func (t *turn) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
}

// ensure creates the turn's agent message if it doesn't exist yet.
func (t *turn) ensure(ctx context.Context, status string) (store.AppMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.msg != nil {
		return *t.msg, nil
	}
	m, err := t.c.Store.AddAppMessage(ctx, store.AppMessage{Thread: t.agent, Role: store.RoleAgent, Status: status, Job: t.job})
	if err != nil {
		return m, err
	}
	t.msg = &m
	if !t.hidden {
		t.c.publishMessage(m)
	}
	return m, nil
}

func (t *turn) update(ctx context.Context, f func(m *store.AppMessage)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.msg == nil {
		return fmt.Errorf("no message to update")
	}
	next := *t.msg
	f(&next)
	m, err := t.c.Store.UpdateAppMessage(ctx, next)
	if err != nil {
		return err
	}
	t.msg = &m
	if !t.hidden {
		t.c.publishMessage(m)
	}
	return nil
}

// reply is the final text without streaming, or a later note in the same
// turn (e.g. "Couldn't send file"), which gets its own message.
func (t *turn) reply(ctx context.Context, text string) error {
	ctx, cancel := t.ctx(ctx)
	defer cancel()
	t.mu.Lock()
	finished := t.msg != nil && t.msg.Status != store.StatusStreaming
	t.mu.Unlock()
	if finished {
		if !t.hidden {
			_, err := t.c.Post(ctx, t.agent, store.RoleSystem, text, t.job)
			return err
		}
		_, err := t.c.Store.AddAppMessage(ctx, store.AppMessage{Thread: t.agent, Role: store.RoleSystem, Text: text, Job: t.job})
		return err
	}
	if _, err := t.ensure(ctx, store.StatusStreaming); err != nil {
		return err
	}
	return t.finish(ctx, text)
}

func (t *turn) finish(ctx context.Context, text string) error {
	body, actions := ExtractButtons(text)
	if body == "📎" { // the router's stand-in for "files only"
		body = ""
	}
	status := store.StatusDone
	if isFailure(text) {
		status = store.StatusError
	}
	err := t.update(ctx, func(m *store.AppMessage) {
		m.Text, m.Actions, m.Status = body, actions, status
	})
	if err == nil {
		t.notifyFinal()
	}
	return err
}

// File-only turns can finish without a text reply; OnDone also calls this.
func (t *turn) notifyFinal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hidden || t.notified || t.msg == nil || t.msg.Status == store.StatusStreaming {
		return
	}
	if t.msg.Text == "" && len(t.msg.Files) == 0 {
		return
	}
	t.notified = true
	t.c.notify(*t.msg)
}

func isFailure(text string) bool {
	for _, p := range []string{"Something went wrong:", "All models are unavailable", "That took longer than", "Couldn't "} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// stream pushes the growing reply to clients as delta events, at most every
// StreamInterval. The database only sees the final text.
func (t *turn) stream(ctx context.Context) *router.ReplyStream {
	var mu sync.Mutex
	var buf strings.Builder
	var last time.Time
	var id int64
	push := func(force bool) {
		if id == 0 {
			return
		}
		if !force && t.c.Now().Sub(last) < t.c.StreamInterval {
			return
		}
		last = t.c.Now()
		text, _ := ExtractButtons(buf.String())
		t.c.hub.publish(Event{Type: "delta", ID: id, Thread: t.agent, Text: text})
	}
	return &router.ReplyStream{
		Append: func(s string) {
			mu.Lock()
			defer mu.Unlock()
			if id == 0 {
				cctx, cancel := t.ctx(ctx)
				m, err := t.ensure(cctx, store.StatusStreaming)
				cancel()
				if err != nil {
					t.c.log.Error("create streaming message", "err", err)
					return
				}
				id = m.ID
				t.c.setState(t.agent, AgentState{Working: true, Detail: "Writing", Since: t.c.Now()})
			}
			buf.WriteString(s)
			push(false)
		},
		Reset: func() {
			mu.Lock()
			defer mu.Unlock()
			buf.Reset()
			push(true)
		},
		Finish: func(ctx context.Context, final string) error {
			mu.Lock()
			defer mu.Unlock()
			cctx, cancel := t.ctx(ctx)
			defer cancel()
			if _, err := t.ensure(cctx, store.StatusStreaming); err != nil {
				return err
			}
			return t.finish(cctx, final)
		},
	}
}

// sendFile copies a file an agent named with MEDIA: into the files dir and
// attaches it to this turn's reply.
func (t *turn) sendFile(ctx context.Context, path string) error {
	ctx, cancel := t.ctx(ctx)
	defer cancel()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	af, err := t.c.saveFile(ctx, t.agent, filepath.Base(path), "", f)
	if err != nil {
		return err
	}
	if _, err := t.ensure(ctx, store.StatusDone); err != nil {
		return err
	}
	err = t.update(ctx, func(m *store.AppMessage) { m.Files = append(m.Files, af) })
	if err == nil {
		t.notifyFinal()
	}
	return err
}

// saveFile stores r under a new ID and records it.
func (c *Channel) saveFile(ctx context.Context, thread, name, mimeType string, r io.Reader) (store.AppFile, error) {
	id := newID(12)
	path, size, err := attachments.Save(filepath.Join(c.FilesDir, thread), id+"-"+attachments.SafeName(name), r, MaxFileSize)
	if err != nil {
		return store.AppFile{}, err
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
			mimeType = t
		} else {
			mimeType = "application/octet-stream"
		}
	}
	af := store.AppFile{ID: id, Thread: thread, Name: attachments.SafeName(name), Mime: mimeType, Size: size, Path: path}
	if err := c.Store.AddAppFile(ctx, af); err != nil {
		os.Remove(path)
		return store.AppFile{}, err
	}
	return af, nil
}

// fetcher copies files the user attached into the agent's attachments dir.
func (c *Channel) fetcher(files []store.AppFile) func(ctx context.Context, dir string) ([]provider.Attachment, error) {
	return func(ctx context.Context, dir string) ([]provider.Attachment, error) {
		var out []provider.Attachment
		for _, f := range files {
			src, err := os.Open(f.Path)
			if err != nil {
				return out, fmt.Errorf("%s: %w", f.Name, err)
			}
			path, size, err := attachments.Save(dir, f.Name, src, MaxFileSize)
			src.Close()
			if err != nil {
				return out, fmt.Errorf("%s: %w", f.Name, err)
			}
			out = append(out, provider.Attachment{Path: path, Name: f.Name, MimeType: f.Mime, Kind: kindOf(f.Mime), Size: size})
		}
		return out, nil
	}
}

func kindOf(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return provider.KindImage
	case strings.HasPrefix(mimeType, "audio/"):
		return provider.KindAudio
	default:
		return provider.KindFile
	}
}

// agentNames returns the configured agents, sorted.
func (c *Channel) agentNames() []string {
	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
