package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

	me, err := b.Client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram bot %q: %w", b.Name, err)
	}
	log = log.With("username", me.Username)
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
		ups, err := b.Client.GetUpdates(ctx, offset, b.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
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
	if m.MediaGroupID != "" {
		b.addToGroup(m.Chat.ID, m.MediaGroupID, text, refs)
		return
	}
	b.submit(ctx, m.Chat.ID, text, refs)
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
