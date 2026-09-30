package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/kyleparisi/ganclaw/internal/telegram"
)

// sentMessages fakes the Telegram API and records sendMessage texts.
type sentMessages struct {
	mu   sync.Mutex
	msgs []string
}

func (s *sentMessages) client() *telegram.Client {
	return &telegram.Client{Token: "1:t", HTTP: telegram.HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
		var body struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			s.mu.Lock()
			s.msgs = append(s.msgs, body.Text)
			s.mu.Unlock()
		}
		return &http.Response{StatusCode: 200, Body: jsonBody(`{"ok":true,"result":{"message_id":1}}`)}, nil
	}}}
}

func (s *sentMessages) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}

type runFunc func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error)

func newGateway(t *testing.T, sent *sentMessages, run runFunc) (*Gateway, *store.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	db := store.NewTestStore(t)
	agents := map[string]agent.Agent{
		"assistant": {Name: "assistant", Workspace: "/ws/assistant"},
		"support":   {Name: "support", Workspace: "/ws/support"},
	}
	r := router.New(ctx, router.Config{Agents: agents, Store: db, Run: run})
	t.Cleanup(func() { cancel(); r.Wait() })
	return &Gateway{
		Ctx:    ctx,
		Router: r,
		Agents: agents,
		Bots:   map[string]*telegram.Bot{"assistant": {Name: "assistant", Agent: "assistant", Client: sent.client()}},
	}, db
}

func TestGatewaySend(t *testing.T) {
	t.Run("Sends through the named bot", func(t *testing.T) {
		sent := &sentMessages{}
		subject, _ := newGateway(t, sent, nil)

		err := subject.Send(context.Background(), api.Target{Channel: "telegram", Bot: "assistant", Chat: 111}, "hello")

		require.NoError(t, err)
		assert.Equal(t, []string{"hello"}, sent.all())
	})

	t.Run("Unknown bot", func(t *testing.T) {
		subject, _ := newGateway(t, &sentMessages{}, nil)

		err := subject.Send(context.Background(), api.Target{Bot: "ghost"}, "hello")

		var e *api.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, api.CodeUnknownBot, e.Code)
	})
}

func TestGatewayRun(t *testing.T) {
	ctx := context.Background()

	t.Run("Delivered run streams to the chat and joins its conversation", func(t *testing.T) {
		sent := &sentMessages{}
		subject, db := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			assert.Equal(t, "/ws/assistant", req.Cwd)
			sessions["claude"] = "k-1"
			return "claude", provider.Result{Text: "New lead: looks legit."}, nil
		})
		target := &api.Target{Channel: "telegram", Bot: "assistant", Chat: 111}

		res, err := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "review lead"}, target)

		require.NoError(t, err)
		assert.Equal(t, "claude", res.Provider)
		assert.Equal(t, "New lead: looks legit.", res.Text)
		assert.Equal(t, []string{"New lead: looks legit."}, sent.all())
		s, _ := db.Sessions(ctx, "telegram:assistant:111")
		assert.Equal(t, provider.Sessions{"claude": "k-1"}, s, "the user can reply in Telegram with context")
	})

	t.Run("Delivered run with a session keeps its own conversation", func(t *testing.T) {
		sent := &sentMessages{}
		subject, db := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			sessions["claude"] = "k-daily"
			return "claude", provider.Result{Text: "Daily task done."}, nil
		})

		_, err := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "p", Session: "daily-task-2026-09-30"},
			&api.Target{Bot: "assistant", Chat: 111})

		require.NoError(t, err)
		assert.Equal(t, []string{"Daily task done."}, sent.all(), "still delivered to the chat")
		chat, _ := db.Sessions(ctx, "telegram:assistant:111")
		job, _ := db.Sessions(ctx, "job:assistant:daily-task-2026-09-30")
		assert.Empty(t, chat, "the chat's own conversation is untouched")
		assert.Equal(t, provider.Sessions{"claude": "k-daily"}, job)
	})

	t.Run("Another agent on the bot gets its own conversation", func(t *testing.T) {
		subject, db := newGateway(t, &sentMessages{}, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			assert.Equal(t, "/ws/support", req.Cwd)
			sessions["codex"] = "c-z"
			return "codex", provider.Result{Text: "ok"}, nil
		})

		_, err := subject.Run(ctx, api.RunRequest{Agent: "support", Prompt: "p"}, &api.Target{Bot: "assistant", Chat: 111})

		require.NoError(t, err)
		s, _ := db.Sessions(ctx, "telegram:assistant:111:support")
		assert.Equal(t, provider.Sessions{"codex": "c-z"}, s)
	})

	t.Run("Undelivered runs use named or one-off job conversations", func(t *testing.T) {
		sent := &sentMessages{}
		var seen []provider.Sessions
		subject, _ := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			seen = append(seen, provider.Sessions{"claude": sessions["claude"]})
			sessions["claude"] = "k-" + req.Prompt
			return "claude", provider.Result{Text: "reply " + req.Prompt}, nil
		})

		r1, _ := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "1", Session: "lead-triage"}, nil)
		subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "2", Session: "lead-triage"}, nil)
		subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "3"}, nil)

		assert.Equal(t, "reply 1", r1.Text)
		assert.Equal(t, []provider.Sessions{{"claude": ""}, {"claude": "k-1"}, {"claude": ""}}, seen, "named sessions continue; one-offs start fresh")
		assert.Empty(t, sent.all(), "undelivered runs don't message anyone")
	})

	t.Run("Unknown agent or bot is rejected before queueing", func(t *testing.T) {
		subject, _ := newGateway(t, &sentMessages{}, nil)

		_, err1 := subject.Run(ctx, api.RunRequest{Agent: "ghost", Prompt: "p"}, nil)
		_, err2 := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "p"}, &api.Target{Bot: "ghost", Chat: 1})

		var e *api.Error
		require.ErrorAs(t, err1, &e)
		assert.Equal(t, api.CodeUnknownAgent, e.Code)
		require.ErrorAs(t, err2, &e)
		assert.Equal(t, api.CodeUnknownBot, e.Code)
	})

	t.Run("Caller cancellation stops the turn", func(t *testing.T) {
		started := make(chan struct{})
		subject, _ := newGateway(t, &sentMessages{}, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			close(started)
			<-ctx.Done()
			return "", provider.Result{}, ctx.Err()
		})
		cctx, cancel := context.WithCancel(ctx)
		go func() { <-started; cancel() }()

		res, err := subject.Run(cctx, api.RunRequest{Agent: "assistant", Prompt: "long"}, nil)

		require.NoError(t, err)
		assert.ErrorIs(t, res.Err, context.Canceled)
	})
}

func TestGatewayQuietAndAsync(t *testing.T) {
	ctx := context.Background()
	target := &api.Target{Bot: "assistant", Chat: 111}

	t.Run("Quiet runs drop NO_REPLY and deliver real news once, unstreamed", func(t *testing.T) {
		sent := &sentMessages{}
		replies := []string{"NO_REPLY", " `HEARTBEAT_OK`. ", "Your domain renews tomorrow."}
		i := 0
		subject, _ := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			assert.Nil(t, req.OnDelta, "quiet runs are not streamed")
			r := replies[i]
			i++
			return "claude", provider.Result{Text: r}, nil
		})

		for range replies {
			_, err := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "event", Quiet: true}, target)
			require.NoError(t, err)
		}

		assert.Equal(t, []string{"Your domain renews tomorrow."}, sent.all())
	})

	t.Run("Quiet runs still report failures", func(t *testing.T) {
		sent := &sentMessages{}
		subject, _ := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			return "", provider.Result{}, errors.New("boom")
		})

		subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "event", Quiet: true}, target)

		assert.Equal(t, []string{"Something went wrong: boom"}, sent.all())
	})

	t.Run("Async runs return at once and deliver later", func(t *testing.T) {
		sent := &sentMessages{}
		release := make(chan struct{})
		subject, _ := newGateway(t, sent, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			<-release
			return "claude", provider.Result{Text: "done later"}, nil
		})

		res, err := subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "p", Async: true}, target)

		require.NoError(t, err)
		assert.Equal(t, router.TurnResult{}, res)
		assert.Empty(t, sent.all())
		close(release)
		assert.Eventually(t, func() bool { return len(sent.all()) == 1 }, 2*time.Second, 5*time.Millisecond)
	})

	t.Run("Heartbeat tasks are appended to the prompt", func(t *testing.T) {
		ws := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n- Check email for new invoices\n"), 0o600))
		var prompt string
		subject, _ := newGateway(t, &sentMessages{}, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			prompt = req.Prompt
			return "claude", provider.Result{Text: "NO_REPLY"}, nil
		})
		subject.Agents["assistant"] = agent.Agent{Name: "assistant", Workspace: ws}

		subject.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "event text", Heartbeat: true}, nil)

		assert.Equal(t, "event text\n\nAlso do your standing heartbeat tasks (from HEARTBEAT.md):\n- Check email for new invoices", prompt)
	})
}

func TestWithHeartbeat(t *testing.T) {
	subject := WithHeartbeat

	t.Run("Comment-only or missing files add nothing", func(t *testing.T) {
		ws := t.TempDir()
		assert.Equal(t, "p", subject("p", ws))
		require.NoError(t, os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n# Keep this file empty\n<!-- note -->\n"), 0o600))
		assert.Equal(t, "p", subject("p", ws))
	})
}

func TestAgentBot(t *testing.T) {
	t.Run("Finds the bot serving an agent", func(t *testing.T) {
		subject, _ := newGateway(t, &sentMessages{}, nil)
		assert.Equal(t, "assistant", subject.AgentBot("assistant"))
		assert.Equal(t, "", subject.AgentBot("support"))
	})
}

func TestListAgents(t *testing.T) {
	t.Run("Agents sorted, with their bots, plus contacts", func(t *testing.T) {
		subject, _ := newGateway(t, &sentMessages{}, nil)

		got := subject.ListAgents([]string{"alex"})(context.Background())

		assert.Equal(t, api.AgentsResponse{
			Agents:   []api.AgentInfo{{Name: "assistant", Bot: "assistant"}, {Name: "support"}},
			Contacts: []string{"alex"},
		}, got)
	})
}

func TestJobKey(t *testing.T) {
	t.Run("Named and one-off keys", func(t *testing.T) {
		assert.Equal(t, "job:sales:lead-triage", JobKey("sales", "lead-triage"))
		a, b := JobKey("x", ""), JobKey("x", "")
		assert.NotEqual(t, a, b)
		assert.True(t, strings.HasPrefix(a, "job:x:once:"))
	})
}

var _ = time.Second
