package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// Bot long-polls one Telegram bot and submits allowed private messages to
// the router.
type Bot struct {
	Name       string
	Agent      string
	Client     *Client
	AllowUsers []int64
	Store      *store.Store
	Submit     func(router.Message) bool
	Logger     *slog.Logger
	// PollTimeout is the getUpdates long-poll duration in seconds.
	PollTimeout int
	// Sleep waits between retries; defaults to a context-aware sleep.
	Sleep func(ctx context.Context, d time.Duration)
	// Commands are registered as the bot's "/" menu at startup.
	Commands []router.Command
	// StreamInterval is how often streamed replies are edited; default
	// 1.2s (Telegram allows about one message per second per chat).
	StreamInterval time.Duration
	// GroupDelay is how long to wait for the rest of an album (photos
	// sent together arrive as separate updates); default 1.5s.
	GroupDelay time.Duration

	once   sync.Once
	log    *slog.Logger
	mu     sync.Mutex
	runCtx context.Context
	groups map[string]*pendingGroup

	health Health
}

// Health is a bot's polling state.
type Health struct {
	Running  bool      `json:"running"`
	LastPoll time.Time `json:"last_poll"` // last successful getUpdates
	LastErr  string    `json:"last_error,omitempty"`
}

// Health reports whether the bot is polling successfully.
func (b *Bot) Health() Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.health
}

func (b *Bot) setHealth(f func(h *Health)) {
	b.mu.Lock()
	f(&b.health)
	b.mu.Unlock()
}

// setup applies defaults once, before Run or Message use them.
func (b *Bot) setup() {
	b.once.Do(func() {
		b.log = logx.OrDiscard(b.Logger).With("bot", b.Name)
		if b.PollTimeout == 0 {
			b.PollTimeout = 50
		}
		if b.Sleep == nil {
			b.Sleep = sleep
		}
		if b.StreamInterval == 0 {
			b.StreamInterval = 1200 * time.Millisecond
		}
		if b.GroupDelay == 0 {
			b.GroupDelay = 1500 * time.Millisecond
		}
		b.groups = map[string]*pendingGroup{}
	})
}

// pendingGroup collects the messages of one album.
type pendingGroup struct {
	chatID int64
	text   string
	refs   []fileRef
	timer  *time.Timer
}

// Run polls until ctx is cancelled. It returns an error only if the bot
// cannot start (e.g. invalid token).
func (b *Bot) Run(ctx context.Context) error {
	b.setup()
	log := b.log
	b.mu.Lock()
	b.runCtx = ctx
	b.mu.Unlock()
	defer b.setHealth(func(h *Health) { h.Running = false })

	me, err := b.Client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram bot %q: %w", b.Name, err)
	}
	log = log.With("username", me.Username)
	// getMe reached Telegram: count it, so health is good before the
	// first long poll (up to PollTimeout) completes.
	b.setHealth(func(h *Health) { h.Running = true; h.LastPoll = time.Now(); h.LastErr = "" })
	if len(b.Commands) > 0 {
		cmds := make([]BotCommand, len(b.Commands))
		for i, c := range b.Commands {
			cmds[i] = BotCommand{Command: c.Name, Description: c.Description}
		}
		if err := b.Client.SetMyCommands(ctx, cmds); err != nil {
			log.Warn("set command menu failed", "err", err)
		}
	}
	log.Info("telegram bot started", "agent", b.Agent, "allowed_users", len(b.AllowUsers))

	key := "telegram:" + b.Name
	offset, err := b.Store.Offset(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			return nil // shut down during startup
		}
		return fmt.Errorf("telegram bot %q: load offset: %w", b.Name, err)
	}

	backoff := time.Second
	for ctx.Err() == nil {
		b.setHealth(func(h *Health) { h.Running = true })
		ups, err := b.Client.GetUpdates(ctx, offset, b.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.setHealth(func(h *Health) { h.LastErr = err.Error() })
			wait := backoff
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
				wait = time.Duration(apiErr.RetryAfter) * time.Second
			}
			log.Warn("getUpdates failed", "err", err, "retry_in", wait)
			b.Sleep(ctx, wait)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		b.setHealth(func(h *Health) { h.LastPoll = time.Now(); h.LastErr = "" })
		if len(ups) == 0 {
			continue
		}
		for _, u := range ups {
			offset = max(offset, u.UpdateID+1)
			b.handle(ctx, log, u)
		}
		if err := b.Store.SetOffset(ctx, key, offset); err != nil {
			log.Error("save offset", "err", err)
		}
	}
	log.Info("telegram bot stopped")
	return nil
}

func (b *Bot) handle(ctx context.Context, log *slog.Logger, u Update) {
	m := u.Message
	if m == nil {
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	refs := fileRefs(m)
	if text == "" && len(refs) == 0 {
		return // stickers, locations, etc.
	}
	if m.From == nil || !slices.Contains(b.AllowUsers, m.From.ID) {
		var id int64
		var name string
		if m.From != nil {
			id, name = m.From.ID, m.From.Username
		}
		log.Warn("ignoring message from user not in allow_users", "user_id", id, "user", name, "chat_type", m.Chat.Type)
		return
	}
	if m.Chat.Type != "private" {
		log.Info("ignoring non-private chat", "chat_id", m.Chat.ID, "chat_type", m.Chat.Type)
		return
	}
	text = withReply(m, text)
	if m.MediaGroupID != "" {
		b.addToGroup(m.Chat.ID, m.MediaGroupID, text, refs)
		return
	}
	b.submit(ctx, m.Chat.ID, text, refs)
}

// maxReplyContext caps how much of a replied-to message is included.
const maxReplyContext = 2000

// withReply puts the message being replied to in front of text, so the
// agent knows what "approve" or "yes" answers. Commands are left alone.
func withReply(m *Message, text string) string {
	r := m.ReplyToMessage
	if r == nil || strings.HasPrefix(strings.TrimSpace(text), "/") {
		return text
	}
	who := "the user's earlier message"
	if r.From != nil && r.From.IsBot {
		who = "your earlier message"
	}
	quoted := r.Text
	if quoted == "" {
		quoted = r.Caption
	}
	if m.Quote != nil && m.Quote.Text != "" {
		quoted, who = m.Quote.Text, "this part of "+who
	}
	if quoted == "" {
		quoted = "(a message without text)"
	}
	if runes := []rune(quoted); len(runes) > maxReplyContext {
		quoted = string(runes[:maxReplyContext]) + " …"
	}
	return "[Replying to " + who + ":]\n> " + strings.ReplaceAll(quoted, "\n", "\n> ") + "\n\n" + text
}

// addToGroup buffers an album's messages and submits them as one message
// once no more have arrived for GroupDelay.
func (b *Bot) addToGroup(chatID int64, groupID, text string, refs []fileRef) {
	key := fmt.Sprintf("%d:%s", chatID, groupID)
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.groups[key]
	if !ok {
		g = &pendingGroup{chatID: chatID}
		b.groups[key] = g
		g.timer = time.AfterFunc(b.GroupDelay, func() {
			b.mu.Lock()
			delete(b.groups, key)
			ctx := b.runCtx
			b.mu.Unlock()
			if ctx.Err() == nil {
				b.submit(ctx, g.chatID, g.text, g.refs)
			}
		})
	} else {
		g.timer.Reset(b.GroupDelay)
	}
	if g.text == "" {
		g.text = text
	}
	g.refs = append(g.refs, refs...)
}

func (b *Bot) submit(ctx context.Context, chatID int64, text string, refs []fileRef) {
	msg := b.Message(chatID, b.Agent, text)
	if len(refs) > 0 {
		msg.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			return b.Client.fetch(ctx, refs, dir)
		}
	}
	if !b.Submit(msg) {
		if err := b.Client.SendMessage(ctx, chatID, "I'm busy with earlier messages — please try again in a bit."); err != nil {
			b.log.Error("send busy notice", "err", err)
		}
	}
}

// ChatKey is the conversation key for a chat with this bot. Runs by a
// different agent than the bot's own get their own conversation.
func (b *Bot) ChatKey(chatID int64, agent string) string {
	key := fmt.Sprintf("telegram:%s:%d", b.Name, chatID)
	if agent != "" && agent != b.Agent {
		key += ":" + agent
	}
	return key
}

// Message builds a router message whose replies stream into the chat.
// Used for incoming messages and for API runs delivered to this bot.
func (b *Bot) Message(chatID int64, agent, text string) router.Message {
	b.setup()
	return router.Message{
		ChatKey: b.ChatKey(chatID, agent),
		Agent:   agent,
		Text:    text,
		Reply: func(ctx context.Context, text string) error {
			return b.Client.SendMessage(ctx, chatID, text)
		},
		Typing: func(ctx context.Context) error {
			return b.Client.SendChatAction(ctx, chatID, "typing")
		},
		SendFile: func(ctx context.Context, path string) error {
			return b.Client.SendFile(ctx, chatID, path)
		},
		Stream: func(ctx context.Context) *router.ReplyStream {
			return newStream(ctx, b.Client, chatID, b.StreamInterval, b.log).replyStream()
		},
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
