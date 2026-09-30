package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// fakeTelegram answers Bot API calls. Polls returns the update batches for
// successive getUpdates calls; once exhausted, getUpdates blocks until the
// request is cancelled.
type fakeTelegram struct {
	Polls     [][]Update
	PollErrs  []error // returned (in order) before any Polls
	GetMeErr  error
	OnSend    func(chatID int64, text string)
	OnAction  func(chatID int64, action string)
	OnPoll    func(offset int64)
	OnCmds    func(cmds []any)
	mu        sync.Mutex
	pollCount int
}

func (f *fakeTelegram) Client() *Client {
	return &Client{Token: testToken, HTTP: HTTPClient{Do: f.do}}
}

func (f *fakeTelegram) do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	_ = json.NewDecoder(req.Body).Decode(&body)
	switch {
	case strings.HasSuffix(req.URL.Path, "/getMe"):
		if f.GetMeErr != nil {
			return nil, f.GetMeErr
		}
		return okResponse(User{ID: 999, IsBot: true, Username: "test_bot"}), nil
	case strings.HasSuffix(req.URL.Path, "/getUpdates"):
		if f.OnPoll != nil {
			f.OnPoll(int64(body["offset"].(float64)))
		}
		f.mu.Lock()
		n := f.pollCount
		f.pollCount++
		f.mu.Unlock()
		if n < len(f.PollErrs) {
			return nil, f.PollErrs[n]
		}
		n -= len(f.PollErrs)
		if n < len(f.Polls) {
			return okResponse(f.Polls[n]), nil
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	case strings.HasSuffix(req.URL.Path, "/sendMessage"):
		if f.OnSend != nil {
			f.OnSend(int64(body["chat_id"].(float64)), body["text"].(string))
		}
		return okResponse(map[string]any{"message_id": 1}), nil
	case strings.HasSuffix(req.URL.Path, "/setMyCommands"):
		if f.OnCmds != nil {
			f.OnCmds(body["commands"].([]any))
		}
		return okResponse(true), nil
	case strings.HasSuffix(req.URL.Path, "/sendChatAction"):
		if f.OnAction != nil {
			f.OnAction(int64(body["chat_id"].(float64)), body["action"].(string))
		}
		return okResponse(true), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: jsonBody(map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})}, nil
}

func privateMsg(updateID, userID int64, text string) Update {
	return Update{UpdateID: updateID, Message: &Message{
		MessageID: updateID, Text: text,
		From: &User{ID: userID, Username: "user"},
		Chat: Chat{ID: userID, Type: "private"},
	}}
}

// runBot runs the bot until stop is called, returning Run's error.
func runBot(b *Bot) (stop func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- b.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-errc:
			return err
		case <-time.After(2 * time.Second):
			return errors.New("bot did not stop")
		}
	}
}

func TestBotRun(t *testing.T) {
	t.Run("SuccessFlow", func(t *testing.T) {
		db := store.NewTestStore(t)
		submitted := make(chan router.Message, 4)
		sent := make(chan string, 4)
		polls := make(chan int64, 4)
		tg := &fakeTelegram{
			Polls:  [][]Update{{privateMsg(10, 111, "hello")}},
			OnPoll: func(offset int64) { polls <- offset },
			OnSend: func(chatID int64, text string) {
				assert.Equal(t, int64(111), chatID)
				sent <- text
			},
		}
		subject := &Bot{
			Name: "assistant", Agent: "assistant", Client: tg.Client(),
			AllowUsers: []int64{111}, Store: db,
			Submit: func(m router.Message) bool { submitted <- m; return true },
		}
		stop := runBot(subject)

		m := <-submitted
		assert.Equal(t, "telegram:assistant:111", m.ChatKey)
		assert.Equal(t, "assistant", m.Agent)
		assert.Equal(t, "hello", m.Text)
		require.NoError(t, m.Reply(context.Background(), "hi back"))
		assert.Equal(t, "hi back", <-sent)

		assert.Equal(t, int64(0), <-polls)
		assert.Equal(t, int64(11), <-polls, "next poll acknowledges the update")
		require.NoError(t, stop())
		off, err := db.Offset(context.Background(), "telegram:assistant")
		require.NoError(t, err)
		assert.Equal(t, int64(11), off)
	})

	t.Run("Resumes from the saved offset", func(t *testing.T) {
		db := store.NewTestStore(t)
		require.NoError(t, db.SetOffset(context.Background(), "telegram:assistant", 500))
		polls := make(chan int64, 1)
		tg := &fakeTelegram{OnPoll: func(offset int64) { polls <- offset }}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: db,
			Submit: func(router.Message) bool { return true }}
		stop := runBot(subject)

		assert.Equal(t, int64(500), <-polls)
		require.NoError(t, stop())
	})

	t.Run("Users not in allow_users are ignored", func(t *testing.T) {
		db := store.NewTestStore(t)
		polls := make(chan int64, 4)
		tg := &fakeTelegram{
			Polls:  [][]Update{{privateMsg(1, 666, "let me in"), {UpdateID: 2, Message: &Message{Text: "no sender", Chat: Chat{ID: 5, Type: "private"}}}}},
			OnPoll: func(offset int64) { polls <- offset },
			OnSend: func(int64, string) { t.Error("must not reply to strangers") },
		}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: db,
			Submit: func(router.Message) bool { t.Error("must not submit"); return true }}
		stop := runBot(subject)

		<-polls
		assert.Equal(t, int64(3), <-polls, "ignored updates are still acknowledged")
		require.NoError(t, stop())
	})

	t.Run("Group chats are ignored even from allowed users", func(t *testing.T) {
		db := store.NewTestStore(t)
		polls := make(chan int64, 4)
		group := Update{UpdateID: 1, Message: &Message{Text: "hi all", From: &User{ID: 111}, Chat: Chat{ID: -100, Type: "supergroup"}}}
		tg := &fakeTelegram{Polls: [][]Update{{group}}, OnPoll: func(o int64) { polls <- o }}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: db,
			Submit: func(router.Message) bool { t.Error("must not submit group messages"); return true }}
		stop := runBot(subject)

		<-polls
		<-polls
		require.NoError(t, stop())
	})

	t.Run("Non-text messages are skipped", func(t *testing.T) {
		db := store.NewTestStore(t)
		polls := make(chan int64, 4)
		sticker := Update{UpdateID: 1, Message: &Message{From: &User{ID: 111}, Chat: Chat{ID: 111, Type: "private"}}}
		tg := &fakeTelegram{Polls: [][]Update{{sticker, {UpdateID: 2}}}, OnPoll: func(o int64) { polls <- o }}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: db,
			Submit: func(router.Message) bool { t.Error("must not submit"); return true }}
		stop := runBot(subject)

		<-polls
		assert.Equal(t, int64(3), <-polls)
		require.NoError(t, stop())
	})

	t.Run("Busy notice when the router rejects a message", func(t *testing.T) {
		sent := make(chan string, 1)
		tg := &fakeTelegram{
			Polls:  [][]Update{{privateMsg(1, 111, "one more")}},
			OnSend: func(chatID int64, text string) { sent <- text },
		}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(router.Message) bool { return false }}
		stop := runBot(subject)

		assert.Contains(t, <-sent, "I'm busy")
		require.NoError(t, stop())
	})

	t.Run("Typing callback sends a chat action", func(t *testing.T) {
		submitted := make(chan router.Message, 1)
		actions := make(chan string, 1)
		tg := &fakeTelegram{
			Polls: [][]Update{{privateMsg(1, 111, "hello")}},
			OnAction: func(chatID int64, action string) {
				assert.Equal(t, int64(111), chatID)
				actions <- action
			},
		}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(m router.Message) bool { submitted <- m; return true }}
		stop := runBot(subject)

		m := <-submitted
		require.NoError(t, m.Typing(context.Background()))
		assert.Equal(t, "typing", <-actions)
		require.NoError(t, stop())
	})

	t.Run("Command menu is registered at startup", func(t *testing.T) {
		cmds := make(chan []any, 1)
		tg := &fakeTelegram{OnCmds: func(c []any) { cmds <- c }}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit:   func(router.Message) bool { return true },
			Commands: []router.Command{{Name: "new", Description: "Start over"}, {Name: "stop", Description: "Stop"}}}
		stop := runBot(subject)

		assert.Equal(t, []any{
			map[string]any{"command": "new", "description": "Start over"},
			map[string]any{"command": "stop", "description": "Stop"},
		}, <-cmds)
		require.NoError(t, stop())
	})

	t.Run("Stream callback streams into the chat", func(t *testing.T) {
		submitted := make(chan router.Message, 1)
		sent := make(chan string, 2)
		tg := &fakeTelegram{
			Polls:  [][]Update{{privateMsg(1, 111, "hello")}},
			OnSend: func(chatID int64, text string) { sent <- text },
		}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(m router.Message) bool { submitted <- m; return true }}
		stop := runBot(subject)

		m := <-submitted
		rs := m.Stream(context.Background())
		require.NoError(t, rs.Finish(context.Background(), "streamed answer"))
		assert.Equal(t, "streamed answer", <-sent)
		require.NoError(t, stop())
	})

	t.Run("Photo with caption is submitted with a fetcher", func(t *testing.T) {
		submitted := make(chan router.Message, 1)
		photo := Update{UpdateID: 1, Message: &Message{
			From: &User{ID: 111}, Chat: Chat{ID: 111, Type: "private"},
			Caption: "what is this?", Photo: []PhotoSize{{FileID: "p-small"}, {FileID: "p-big", FileSize: 1000}},
		}}
		tg := &fakeTelegram{Polls: [][]Update{{photo}}}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(m router.Message) bool { submitted <- m; return true }}
		stop := runBot(subject)

		m := <-submitted
		assert.Equal(t, "what is this?", m.Text)
		assert.NotNil(t, m.Fetch)
		require.NoError(t, stop())
	})

	t.Run("Albums are combined into one message", func(t *testing.T) {
		submitted := make(chan router.Message, 2)
		album := func(id int64, caption, fileID string) Update {
			return Update{UpdateID: id, Message: &Message{
				From: &User{ID: 111}, Chat: Chat{ID: 111, Type: "private"}, MediaGroupID: "g1",
				Caption: caption, Photo: []PhotoSize{{FileID: fileID}},
			}}
		}
		tg := &fakeTelegram{Polls: [][]Update{{album(1, "", "a")}, {album(2, "compare these", "b"), album(3, "", "c")}}}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			GroupDelay: 50 * time.Millisecond,
			Submit:     func(m router.Message) bool { submitted <- m; return true }}
		stop := runBot(subject)

		m := <-submitted
		assert.Equal(t, "compare these", m.Text)
		require.NotNil(t, m.Fetch)
		select {
		case extra := <-submitted:
			t.Errorf("album split into several messages: %q", extra.Text)
		case <-time.After(150 * time.Millisecond):
		}
		require.NoError(t, stop())

		subject.mu.Lock()
		defer subject.mu.Unlock()
		assert.Empty(t, subject.groups)
	})

	t.Run("Poll errors back off, honouring retry_after", func(t *testing.T) {
		var mu sync.Mutex
		var waits []time.Duration
		polls := make(chan int64, 8)
		tg := &fakeTelegram{
			PollErrs: []error{
				errors.New("network down"),
				errors.New("still down"),
				&APIError{Method: "getUpdates", Code: 429, Description: "slow down", RetryAfter: 7},
			},
			OnPoll: func(o int64) { polls <- o },
		}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(router.Message) bool { return true },
			Sleep: func(ctx context.Context, d time.Duration) {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
			}}
		stop := runBot(subject)

		for i := 0; i < 4; i++ {
			<-polls
		}
		require.NoError(t, stop())
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 7 * time.Second}, waits)
	})

	t.Run("Health tracks successful polls and errors", func(t *testing.T) {
		polls := make(chan int64, 8)
		tg := &fakeTelegram{
			PollErrs: []error{errors.New("network down")},
			Polls:    [][]Update{{}},
			OnPoll:   func(o int64) { polls <- o },
		}
		subject := &Bot{Name: "support", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(router.Message) bool { return true },
			Sleep:  func(context.Context, time.Duration) {}}
		assert.Equal(t, Health{}, subject.Health())
		stop := runBot(subject)

		<-polls // fails
		<-polls // succeeds
		<-polls // third poll started: second's result recorded
		h := subject.Health()
		assert.True(t, h.Running)
		assert.False(t, h.LastPoll.IsZero())
		assert.Empty(t, h.LastErr, "a successful poll clears the error")
		require.NoError(t, stop())
		assert.False(t, subject.Health().Running)
	})

	t.Run("Invalid token fails at startup", func(t *testing.T) {
		tg := &fakeTelegram{GetMeErr: errors.New("401 Unauthorized")}
		subject := &Bot{Name: "assistant", Client: tg.Client(), AllowUsers: []int64{111}, Store: store.NewTestStore(t),
			Submit: func(router.Message) bool { return true }}

		err := subject.Run(context.Background())

		assert.ErrorContains(t, err, `telegram bot "assistant"`)
		assert.ErrorContains(t, err, "401")
	})
}
