package api

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

var testResolver = Resolver{
	Contacts: []config.Contact{{Name: "alex", TelegramChat: 111, DefaultBot: "assistant"}, {Name: "nobot", TelegramChat: 222}},
	Bots:     []string{"assistant", "support"},
}

// serve starts s on a socket in a temp dir and returns a client for it.
func serve(t *testing.T, s *Server) *Client {
	t.Helper()
	// Unix socket paths are limited to ~100 bytes; t.TempDir can be long.
	dir, err := os.MkdirTemp("", "gc")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, sock) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", sock)
		if err == nil {
			c.Close()
		}
		return err == nil
	}, 2*time.Second, 5*time.Millisecond)
	return NewClient(sock)
}

func TestResolver(t *testing.T) {
	subject := testResolver.Resolve

	t.Run("Contact uses its default bot", func(t *testing.T) {
		got, err := subject("alex", "")
		require.NoError(t, err)
		assert.Equal(t, Target{Channel: "telegram", Bot: "assistant", Chat: 111}, got)
	})

	t.Run("Via overrides the bot", func(t *testing.T) {
		got, err := subject("alex", "support")
		require.NoError(t, err)
		assert.Equal(t, "telegram:support:111", got.String())
	})

	t.Run("Raw addresses work without contacts", func(t *testing.T) {
		got, err := subject("telegram:support:-100", "")
		require.NoError(t, err)
		assert.Equal(t, Target{Channel: "telegram", Bot: "support", Chat: -100}, got)
	})

	t.Run("App addresses name an agent's thread", func(t *testing.T) {
		got, err := subject("app:infra", "ignored")
		require.NoError(t, err)
		assert.Equal(t, Target{Channel: "app", Bot: "infra"}, got)
		assert.Equal(t, "app:infra", got.String())
	})

	t.Run("Errors carry stable codes", func(t *testing.T) {
		cases := map[string][2]string{
			"empty app agent": {"app:", CodeUnknownContact},
			"bad app address": {"app:a:b", CodeUnknownContact},
			"missing to":      {"", CodeBadRequest},
			"unknown contact": {"stranger", CodeUnknownContact},
			"no default bot":  {"nobot", CodeBadRequest},
			"unknown bot":     {"telegram:ghost:1", CodeUnknownBot},
		}
		for name, c := range cases {
			_, err := subject(c[0], "")
			var e *Error
			require.ErrorAs(t, err, &e, name)
			assert.Equal(t, c[1], e.Code, name)
		}
	})
}

func TestServerSend(t *testing.T) {
	ctx := context.Background()

	t.Run("SuccessFlow", func(t *testing.T) {
		var sent []string
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Send: func(ctx context.Context, tg Target, text string) error {
				sent = append(sent, tg.String()+" "+text)
				return nil
			},
		})

		resp, err := client.Send(ctx, SendRequest{To: "alex", Text: "disk is 90% full"})

		require.NoError(t, err)
		assert.Equal(t, SendResponse{OK: true, Target: "telegram:assistant:111"}, resp)
		assert.Equal(t, []string{"telegram:assistant:111 disk is 90% full"}, sent)
	})

	t.Run("Unknown contact is a 404 with a code", func(t *testing.T) {
		client := serve(t, &Server{Resolve: testResolver.Resolve})

		_, err := client.Send(ctx, SendRequest{To: "stranger", Text: "hi"})

		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, 404, e.Status)
		assert.Equal(t, CodeUnknownContact, e.Code)
	})

	t.Run("Empty text and unknown fields are bad requests", func(t *testing.T) {
		client := serve(t, &Server{Resolve: testResolver.Resolve})

		_, err := client.Send(ctx, SendRequest{To: "alex"})
		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeBadRequest, e.Code)

		err = client.do(ctx, "POST", "/v1/send", map[string]any{"to": "alex", "text": "x", "surprise": 1}, &SendResponse{})
		require.ErrorAs(t, err, &e)
		assert.Contains(t, e.Message, "unknown field")
	})

	t.Run("Send failures are reported", func(t *testing.T) {
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Send:    func(context.Context, Target, string) error { return errors.New("chat not found") },
		})

		_, err := client.Send(ctx, SendRequest{To: "alex", Text: "x"})

		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeFailed, e.Code)
		assert.Contains(t, e.Message, "chat not found")
	})

	t.Run("Idempotency key sends once and replays the response", func(t *testing.T) {
		var sends atomic.Int32
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Send:    func(context.Context, Target, string) error { sends.Add(1); return nil },
			Store:   store.NewTestStore(t),
		})
		req := SendRequest{To: "alex", Text: "daily report", IdempotencyKey: "report-2026-09-30"}

		first, err := client.Send(ctx, req)
		require.NoError(t, err)
		second, err := client.Send(ctx, req)
		require.NoError(t, err)

		assert.Equal(t, int32(1), sends.Load())
		assert.False(t, first.Replayed)
		assert.True(t, second.Replayed)
		assert.Equal(t, first.Target, second.Target)
	})

	t.Run("Failed attempts release the key for a retry", func(t *testing.T) {
		var calls atomic.Int32
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Send: func(context.Context, Target, string) error {
				if calls.Add(1) == 1 {
					return errors.New("telegram down")
				}
				return nil
			},
			Store: store.NewTestStore(t),
		})
		req := SendRequest{To: "alex", Text: "x", IdempotencyKey: "k1"}

		_, err := client.Send(ctx, req)
		require.Error(t, err)
		resp, err := client.Send(ctx, req)

		require.NoError(t, err)
		assert.False(t, resp.Replayed)
		assert.Equal(t, int32(2), calls.Load())
	})
}

func TestServerRun(t *testing.T) {
	ctx := context.Background()

	t.Run("Delivered run resolves the target and returns the reply", func(t *testing.T) {
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Run: func(ctx context.Context, req RunRequest, tg *Target) (router.TurnResult, error) {
				require.NotNil(t, tg)
				assert.Equal(t, "telegram:support:111", tg.String())
				assert.Equal(t, "support", req.Agent)
				assert.Equal(t, "triage this ticket", req.Prompt)
				return router.TurnResult{Provider: "claude", Text: "Looks like spam."}, nil
			},
		})

		resp, err := client.Run(ctx, RunRequest{Agent: "support", Prompt: "triage this ticket", To: "alex", Via: "support"})

		require.NoError(t, err)
		assert.Equal(t, RunResponse{OK: true, Provider: "claude", Text: "Looks like spam.", Target: "telegram:support:111"}, resp)
	})

	t.Run("Contacts get the agent's own bot unless via is given", func(t *testing.T) {
		var got []string
		client := serve(t, &Server{
			Resolve:  testResolver.Resolve,
			AgentBot: func(agent string) string { return map[string]string{"support": "support"}[agent] },
			Run: func(ctx context.Context, req RunRequest, tg *Target) (router.TurnResult, error) {
				got = append(got, tg.String())
				return router.TurnResult{Text: "ok"}, nil
			},
		})

		client.Run(ctx, RunRequest{Agent: "support", Prompt: "p", To: "alex"})
		client.Run(ctx, RunRequest{Agent: "support", Prompt: "p", To: "alex", Via: "assistant"})
		client.Run(ctx, RunRequest{Agent: "other", Prompt: "p", To: "alex"})
		client.Run(ctx, RunRequest{Agent: "support", Prompt: "p", To: "telegram:assistant:5"})

		assert.Equal(t, []string{
			"telegram:support:111",   // agent's bot
			"telegram:assistant:111", // explicit via wins
			"telegram:assistant:111", // no bot for agent: contact default
			"telegram:assistant:5",   // raw addresses are exact
		}, got)
	})

	t.Run("Async runs return queued immediately", func(t *testing.T) {
		client := serve(t, &Server{
			Resolve: testResolver.Resolve,
			Run: func(ctx context.Context, req RunRequest, tg *Target) (router.TurnResult, error) {
				assert.True(t, req.Async)
				return router.TurnResult{}, nil
			},
		})

		resp, err := client.Run(ctx, RunRequest{Agent: "a", Prompt: "p", To: "alex", Async: true})

		require.NoError(t, err)
		assert.True(t, resp.Queued)
		assert.Equal(t, "telegram:assistant:111", resp.Target)
	})

	t.Run("Undelivered run passes the session and no target", func(t *testing.T) {
		client := serve(t, &Server{
			Run: func(ctx context.Context, req RunRequest, tg *Target) (router.TurnResult, error) {
				assert.Nil(t, tg)
				assert.Equal(t, "lead-triage", req.Session)
				return router.TurnResult{Provider: "codex", Text: "ok"}, nil
			},
		})

		resp, err := client.Run(ctx, RunRequest{Agent: "sales", Prompt: "p", Session: "lead-triage"})

		require.NoError(t, err)
		assert.Empty(t, resp.Target)
	})

	t.Run("Turn failures map to codes", func(t *testing.T) {
		cases := map[string]struct {
			err  error
			code string
		}{
			"unavailable": {errors.Join(provider.ErrUnavailable, errors.New("limits")), CodeUnavailable},
			"timeout":     {context.DeadlineExceeded, CodeTimeout},
			"other":       {errors.New("boom"), CodeFailed},
		}
		for name, c := range cases {
			client := serve(t, &Server{Run: func(context.Context, RunRequest, *Target) (router.TurnResult, error) {
				return router.TurnResult{Text: "shown to user", Err: c.err}, nil
			}})

			_, err := client.Run(ctx, RunRequest{Agent: "a", Prompt: "p"})

			var e *Error
			require.ErrorAs(t, err, &e, name)
			assert.Equal(t, c.code, e.Code, name)
			assert.Equal(t, "shown to user", e.Message, name)
		}
	})

	t.Run("Errors before the run keep their codes", func(t *testing.T) {
		client := serve(t, &Server{Run: func(context.Context, RunRequest, *Target) (router.TurnResult, error) {
			return router.TurnResult{}, Errorf(CodeUnknownAgent, "no agent named %q", "ghost")
		}})

		_, err := client.Run(ctx, RunRequest{Agent: "ghost", Prompt: "p"})

		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeUnknownAgent, e.Code)
		assert.Equal(t, 404, e.Status)
	})

	t.Run("timeout_seconds bounds the run", func(t *testing.T) {
		client := serve(t, &Server{Run: func(ctx context.Context, req RunRequest, tg *Target) (router.TurnResult, error) {
			<-ctx.Done()
			return router.TurnResult{Text: "stopped", Err: ctx.Err()}, nil
		}})

		_, err := client.Run(ctx, RunRequest{Agent: "a", Prompt: "p", TimeoutSeconds: 1})

		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeTimeout, e.Code)
	})

	t.Run("Agent and prompt are required", func(t *testing.T) {
		client := serve(t, &Server{})

		_, err1 := client.Run(ctx, RunRequest{Prompt: "p"})
		_, err2 := client.Run(ctx, RunRequest{Agent: "a"})

		var e *Error
		require.ErrorAs(t, err1, &e)
		assert.Equal(t, CodeBadRequest, e.Code)
		require.ErrorAs(t, err2, &e)
		assert.Equal(t, CodeBadRequest, e.Code)
	})
}

func TestServerAgents(t *testing.T) {
	t.Run("Lists agents and contacts", func(t *testing.T) {
		client := serve(t, &Server{Agents: func(context.Context) AgentsResponse {
			return AgentsResponse{Agents: []AgentInfo{{Name: "support", Bot: "support"}}, Contacts: []string{"alex"}}
		}})

		got, err := client.Agents(context.Background())

		require.NoError(t, err)
		assert.Equal(t, AgentsResponse{Agents: []AgentInfo{{Name: "support", Bot: "support"}}, Contacts: []string{"alex"}}, got)
	})
}

func TestServerAppPairing(t *testing.T) {
	ctx := context.Background()

	t.Run("Off without the app channel", func(t *testing.T) {
		client := serve(t, &Server{})
		_, err := client.Pair(ctx, "phone")
		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeBadRequest, e.Code)
		assert.Contains(t, e.Message, "[app] listen")
	})

	t.Run("Pair, list and unpair", func(t *testing.T) {
		devices := []DeviceInfo{{ID: "d1", Name: "phone"}}
		client := serve(t, &Server{
			Pair: func(_ context.Context, name string) (PairResponse, error) {
				return PairResponse{Code: "ABCD-EFGH", URL: "https://g.example"}, nil
			},
			Devices: func(context.Context) ([]DeviceInfo, error) { return devices, nil },
			Unpair: func(_ context.Context, id string) error {
				devices = nil
				return nil
			},
		})

		p, err := client.Pair(ctx, "phone")
		require.NoError(t, err)
		assert.Equal(t, "ABCD-EFGH", p.Code)
		ds, err := client.Devices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "d1", ds[0].ID)
		require.NoError(t, client.Unpair(ctx, "d1"))
		ds, err = client.Devices(ctx)
		require.NoError(t, err)
		assert.Empty(t, ds)
	})
}

func TestServerHealth(t *testing.T) {
	t.Run("Returns the health snapshot", func(t *testing.T) {
		client := serve(t, &Server{Health: func(context.Context) HealthResponse {
			return HealthResponse{Version: "v1", Bots: []BotHealth{{Name: "support", Running: true}}, Probes: []ProbeResult{{Name: "tunnel", OK: true}}}
		}})

		got, err := client.Health(context.Background())

		require.NoError(t, err)
		assert.Equal(t, "v1", got.Version)
		assert.Equal(t, "support", got.Bots[0].Name)
		assert.True(t, got.Probes[0].OK)
	})
}

func TestServerStatus(t *testing.T) {
	t.Run("Returns provider statuses", func(t *testing.T) {
		client := serve(t, &Server{Status: func(context.Context) []provider.Status {
			return []provider.Status{{Provider: "codex", Known: true, Reason: "usage limit reached"}}
		}})

		got, err := client.Status(context.Background())

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "usage limit reached", got[0].Reason)
	})
}

func TestServe(t *testing.T) {
	t.Run("Socket is private and removed on shutdown", func(t *testing.T) {
		dir, _ := os.MkdirTemp("", "gc")
		defer os.RemoveAll(dir)
		sock := filepath.Join(dir, "api.sock")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- (&Server{}).Serve(ctx, sock) }()
		require.Eventually(t, func() bool { _, err := os.Stat(sock); return err == nil }, 2*time.Second, 5*time.Millisecond)

		info, err := os.Stat(sock)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		cancel()
		require.NoError(t, <-done)
		assert.NoFileExists(t, sock)
	})

	t.Run("Stale socket files are replaced", func(t *testing.T) {
		dir, _ := os.MkdirTemp("", "gc")
		defer os.RemoveAll(dir)
		sock := filepath.Join(dir, "api.sock")
		require.NoError(t, os.WriteFile(sock, nil, 0o600))

		client := func() *Client {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- (&Server{Status: func(context.Context) []provider.Status { return nil }}).Serve(ctx, sock)
			}()
			t.Cleanup(func() { cancel(); <-done })
			return NewClient(sock)
		}()

		assert.Eventually(t, func() bool { _, err := client.Status(context.Background()); return err == nil }, 2*time.Second, 5*time.Millisecond)
	})

	t.Run("A socket already in use is not stolen", func(t *testing.T) {
		dir, _ := os.MkdirTemp("", "gc")
		defer os.RemoveAll(dir)
		sock := filepath.Join(dir, "api.sock")
		ln, err := net.Listen("unix", sock)
		require.NoError(t, err)
		defer ln.Close()

		err = (&Server{}).Serve(context.Background(), sock)

		assert.ErrorContains(t, err, "already in use")
	})

	t.Run("Unreachable server is a no_server error", func(t *testing.T) {
		client := NewClient("/nonexistent/ganclaw.sock")

		_, err := client.Status(context.Background())

		var e *Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, CodeNoServer, e.Code)
	})
}

func TestPeerUID(t *testing.T) {
	t.Run("Reports the connecting user", func(t *testing.T) {
		a, b, err := unixPair()
		require.NoError(t, err)
		defer a.Close()
		defer b.Close()

		uid, ok := peerUID(a)

		assert.True(t, ok)
		assert.Equal(t, uint32(os.Getuid()), uid)
		assert.True(t, allowedUIDs()[uid])
	})

	t.Run("Non-unix connections are refused", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()

		_, ok := peerUID(a)

		assert.False(t, ok)
	})
}

func unixPair() (*net.UnixConn, *net.UnixConn, error) {
	dir, err := os.MkdirTemp("", "gc")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "p.sock"), Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()
	client, err := net.DialUnix("unix", nil, ln.Addr().(*net.UnixAddr))
	if err != nil {
		return nil, nil, err
	}
	server, err := ln.AcceptUnix()
	return server, client, err
}

func TestScheduledNotification(t *testing.T) {
	subject := serve
	t.Run("App job reaches the scheduled sender", func(t *testing.T) {
		called := false
		c := subject(t, &Server{Resolve: testResolver.Resolve, SendJob: func(ctx context.Context, target Target, text, job string) error {
			called = true
			assert.Equal(t, "app", target.Channel)
			assert.Equal(t, "support", target.Bot)
			assert.Equal(t, "daily-check", job)
			assert.Equal(t, "All clear", text)
			return nil
		}})
		_, err := c.Send(context.Background(), SendRequest{To: "app:support", Text: "All clear", Job: "daily-check"})
		require.NoError(t, err)
		assert.True(t, called)
	})
	t.Run("Telegram job is rejected before delivery", func(t *testing.T) {
		c := subject(t, &Server{Resolve: testResolver.Resolve, Send: func(context.Context, Target, string) error {
			t.Fatal("invalid job must not deliver")
			return nil
		}})
		_, err := c.Send(context.Background(), SendRequest{To: "alex", Text: "All clear", Job: "daily-check"})
		require.ErrorContains(t, err, "job requires an app destination")
	})
}
