package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

func newTestProvider(t *testing.T, server *fakeAppServer) *Provider {
	t.Helper()
	p, err := New(context.Background(), Config{
		Bin:           "codex-test",
		CodexHome:     "/tmp/codex-home-test",
		Sandbox:       "workspace-write",
		ClientVersion: "1.2.3",
		Exec:          server.Exec(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { p.Close() })
	return p
}

func TestNew(t *testing.T) {
	subject := New

	t.Run("Completes initialize handshake", func(t *testing.T) {
		var clientName, clientVersion string
		server := &fakeAppServer{
			OnInitialize: func(p initializeParams) {
				clientName, clientVersion = p.ClientInfo.Name, p.ClientInfo.Version
			},
		}

		p, err := subject(context.Background(), Config{
			Bin:           "codex-test",
			CodexHome:     "/tmp/codex-home-test",
			ClientVersion: "1.2.3",
			Exec:          server.Exec(),
		})
		require.NoError(t, err)
		defer p.Close()

		assert.Equal(t, "codex-test", server.Cmd.Bin)
		assert.Equal(t, []string{"app-server"}, server.Cmd.Args)
		assert.Contains(t, server.Cmd.Env, "CODEX_HOME=/tmp/codex-home-test")
		assert.Equal(t, "ganclaw", clientName)
		assert.Equal(t, "1.2.3", clientVersion)
		assert.Eventually(t, func() bool {
			return strings.Join(server.Notifications(), ",") == "initialized"
		}, eventually, tick)
	})

	t.Run("Environment comes from Environ with CODEX_HOME replaced", func(t *testing.T) {
		server := &fakeAppServer{}

		p, err := subject(context.Background(), Config{
			CodexHome: "/state/codex",
			Environ:   func() []string { return []string{"PATH=/bin", "CODEX_HOME=/somewhere/else"} },
			Exec:      server.Exec(),
		})
		require.NoError(t, err)
		defer p.Close()

		assert.Equal(t, []string{"PATH=/bin", "CODEX_HOME=/state/codex"}, server.Cmd.Env)
	})

	t.Run("Config overrides are passed as -c flags", func(t *testing.T) {
		server := &fakeAppServer{}

		p, err := subject(context.Background(), Config{
			Overrides: []string{`web_search="live"`, `mcp_servers.browser.command="node"`},
			Exec:      server.Exec(),
		})
		require.NoError(t, err)
		defer p.Close()

		assert.Equal(t, []string{"app-server", "-c", `web_search="live"`, "-c", `mcp_servers.browser.command="node"`}, server.Cmd.Args)
	})

	t.Run("Initialize error is returned", func(t *testing.T) {
		server := &fakeAppServer{
			InitError: &rpcError{Code: -32000, Message: "unsupported client"},
		}

		_, err := subject(context.Background(), Config{Exec: server.Exec()})

		assert.ErrorContains(t, err, "codex initialize")
		assert.ErrorContains(t, err, "unsupported client")
	})
}

func TestProviderRun(t *testing.T) {
	t.Run("SuccessFlow", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					var p threadParams
					assert.NoError(t, json.Unmarshal(params, &p))
					assert.Equal(t, "/work/agent", p.Cwd)
					assert.Equal(t, "Be terse.", p.DeveloperInstructions)
					assert.Equal(t, "never", p.ApprovalPolicy)
					assert.Equal(t, "workspace-write", p.Sandbox)
					assert.Equal(t, "ganclaw", p.ServiceName)
					return threadResult("thread-1"), nil
				case "turn/start":
					var p turnStartParams
					assert.NoError(t, json.Unmarshal(params, &p))
					assert.Equal(t, "thread-1", p.ThreadID)
					assert.Equal(t, []userInput{{Type: "text", Text: "hello?"}}, p.Input)

					s.NotifyAfterReply("item/started", itemEvent("thread-1", "turn-1", agentItem("m1", "commentary", "")))
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m1", "Checking the docs..."))
					s.NotifyAfterReply("item/completed", itemEvent("thread-1", "turn-1", agentItem("m1", "commentary", "Checking the docs...")))
					s.NotifyAfterReply("item/started", itemEvent("thread-1", "turn-1", agentItem("m2", "final_answer", "")))
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m2", "Hel"))
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m2", "lo"))
					s.NotifyAfterReply("item/completed", itemEvent("thread-1", "turn-1", agentItem("m2", "final_answer", "Hello")))
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "completed", nil))
					return turnResult("turn-1"), nil
				}
				return nil, &rpcError{Code: -32601, Message: method}
			},
		}
		subject := newTestProvider(t, server)

		var streamed strings.Builder
		res, err := subject.Run(context.Background(), provider.Request{
			Cwd:          "/work/agent",
			Instructions: "Be terse.",
			Prompt:       "hello?",
			OnDelta:      func(s string) { streamed.WriteString(s) },
		})

		require.NoError(t, err)
		assert.Equal(t, "thread-1", res.Session)
		assert.Equal(t, "Hello", res.Text)
		assert.Equal(t, "Hello", streamed.String(), "commentary must not be streamed")
		assert.Equal(t, []string{"initialize", "thread/start", "turn/start"}, server.Calls())
	})

	t.Run("Images are attached as localImage inputs", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					assert.JSONEq(t, `{"threadId":"thread-1","input":[`+
						`{"type":"text","text":"compare"},`+
						`{"type":"localImage","path":"/ws/a.jpg"},`+
						`{"type":"localImage","path":"/ws/b.png"}]}`, string(params))
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "completed", nil))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{
			Prompt: "compare",
			Attachments: []provider.Attachment{
				{Path: "/ws/a.jpg", Kind: provider.KindImage},
				{Path: "/ws/notes.pdf", Kind: provider.KindFile},
				{Path: "/ws/b.png", Kind: provider.KindImage},
			},
		})

		require.NoError(t, err)
	})

	t.Run("Per-agent settings override model and sandbox", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					var p threadParams
					assert.NoError(t, json.Unmarshal(params, &p))
					assert.Equal(t, "gpt-agent-model", p.Model)
					assert.Equal(t, "danger-full-access", p.Sandbox)
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "completed", nil))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi",
			Settings: provider.Settings{CodexModel: "gpt-agent-model", CodexSandbox: "danger-full-access"}})

		require.NoError(t, err)
	})

	t.Run("Notifications arriving before the turn/start reply are kept", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.Notify("item/completed", itemEvent("thread-1", "turn-1", agentItem("m1", "final_answer", "early")))
					s.Notify("turn/completed", completed("thread-1", "turn-1", "completed", nil))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		res, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		require.NoError(t, err)
		assert.Equal(t, "early", res.Text)
	})

	t.Run("Falls back to last message when phase is unknown", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("item/completed", itemEvent("thread-1", "turn-1", agentItem("m1", "", "first")))
					s.NotifyAfterReply("item/completed", itemEvent("thread-1", "turn-1", agentItem("m2", "", "  second  ")))
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "completed", nil))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		res, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		require.NoError(t, err)
		assert.Equal(t, "second", res.Text)
	})

	t.Run("Resumes an existing session once per process", func(t *testing.T) {
		resumes := 0
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/resume":
					resumes++
					var p threadParams
					assert.NoError(t, json.Unmarshal(params, &p))
					assert.Equal(t, "thread-9", p.ThreadID)
					assert.True(t, p.ExcludeTurns)
					assert.Equal(t, "Be terse.", p.DeveloperInstructions, "current instructions reach resumed threads")
					return threadResult("thread-9"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-9", "turn-x", "completed", nil))
					return turnResult("turn-x"), nil
				}
				return nil, &rpcError{Code: -32601, Message: method}
			},
		}
		subject := newTestProvider(t, server)

		for i := 0; i < 2; i++ {
			res, err := subject.Run(context.Background(), provider.Request{Session: "thread-9", Instructions: "Be terse.", Prompt: "again"})
			require.NoError(t, err)
			assert.Equal(t, "thread-9", res.Session)
		}

		assert.Equal(t, 1, resumes)
		assert.NotContains(t, server.Calls(), "thread/start")
	})

	t.Run("Usage limit triggers fallback", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "failed", "usageLimitExceeded"))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		res, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		var terr *TurnError
		require.ErrorAs(t, err, &terr)
		assert.Equal(t, "usageLimitExceeded", terr.Code)
		assert.Equal(t, "thread-1", res.Session, "session is kept even on failure")
	})

	t.Run("Object-form error code triggers fallback", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "failed",
						map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 401}}))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		var terr *TurnError
		require.ErrorAs(t, err, &terr)
		assert.Equal(t, "httpConnectionFailed", terr.Code)
		assert.True(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("Bad request does not trigger fallback", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "failed", "badRequest"))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		assert.Error(t, err)
		assert.False(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("RPC error on thread/start does not trigger fallback", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				return nil, &rpcError{Code: -32602, Message: "invalid cwd"}
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{Cwd: "/nope", Prompt: "hi"})

		assert.ErrorContains(t, err, "invalid cwd")
		assert.False(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("Process exit mid-turn triggers fallback", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m1", "partial"))
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		// Crash once the turn is visibly underway; the turn never completes.
		_, err := subject.Run(context.Background(), provider.Request{
			Prompt:  "hi",
			OnDelta: func(string) { go server.Crash(errors.New("signal: killed")) },
		})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		assert.ErrorContains(t, err, "exited mid-turn")
	})

	t.Run("Run after process exit triggers fallback", func(t *testing.T) {
		server := &fakeAppServer{}
		subject := newTestProvider(t, server)
		server.Crash(errors.New("exit status 1"))
		<-subject.server().done

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		assert.ErrorContains(t, err, "exit status 1")
	})

	t.Run("App-server is restarted after it exits", func(t *testing.T) {
		server := &fakeAppServer{Handle: answerTurns}
		subject := newTestProvider(t, server)
		server.Crash(errors.New("exit status 1"))
		<-subject.server().done

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})
		require.Error(t, err)
		res, err := subject.Run(context.Background(), provider.Request{Prompt: "hi again"})

		require.NoError(t, err)
		assert.Equal(t, "done", res.Text)
		assert.Equal(t, 2, server.Starts())
	})

	t.Run("Cancelling interrupts the turn", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		interrupted := make(chan turnInterruptParams, 1)
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m1", "working"))
					return turnResult("turn-1"), nil
				case "turn/interrupt":
					var p turnInterruptParams
					assert.NoError(t, json.Unmarshal(params, &p))
					interrupted <- p
					return map[string]any{}, nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(ctx, provider.Request{Prompt: "long task", OnDelta: func(string) { cancel() }})

		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, turnInterruptParams{ThreadID: "thread-1", TurnID: "turn-1"}, <-interrupted)
	})

	t.Run("Approval requests are declined", func(t *testing.T) {
		replies := make(chan message, 1)
		server := &fakeAppServer{
			OnReply: func(s *fakeAppServer, m message) {
				replies <- m
				s.Notify("turn/completed", completed("thread-1", "turn-1", "completed", nil))
			},
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					go s.Request(77, "item/commandExecution/requestApproval", map[string]any{"threadId": "thread-1", "command": "rm -rf /"})
					return turnResult("turn-1"), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "hi"})

		require.NoError(t, err)
		reply := <-replies
		assert.JSONEq(t, `77`, string(reply.ID))
		assert.JSONEq(t, `{"decision":"decline"}`, string(reply.Result))
	})
}

// answerTurns starts or resumes any thread and completes every turn with
// "done".
func answerTurns(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "thread/start", "thread/resume":
		var p threadParams
		_ = json.Unmarshal(params, &p)
		if p.ThreadID == "" {
			p.ThreadID = "thread-new"
		}
		return threadResult(p.ThreadID), nil
	case "turn/start":
		var p turnStartParams
		_ = json.Unmarshal(params, &p)
		s.NotifyAfterReply("item/completed", itemEvent(p.ThreadID, "turn-1", agentItem("m1", "final_answer", "done")))
		s.NotifyAfterReply("turn/completed", completed(p.ThreadID, "turn-1", "completed", nil))
		return turnResult("turn-1"), nil
	}
	return nil, &rpcError{Code: -32601, Message: method}
}

func TestProviderRestart(t *testing.T) {
	newProvider := func(t *testing.T, server *fakeAppServer, cfg Config) *Provider {
		t.Helper()
		cfg.Exec = server.Exec()
		p, err := New(context.Background(), cfg)
		require.NoError(t, err)
		t.Cleanup(func() { p.Close() })
		return p
	}

	t.Run("Cancelled turn restarts the app-server and the thread resumes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				if method == "turn/start" && s.Starts() == 1 {
					s.NotifyAfterReply("item/agentMessage/delta", delta("thread-1", "turn-1", "m1", "working"))
					return turnResult("turn-1"), nil
				}
				if method == "turn/interrupt" {
					return map[string]any{}, nil
				}
				return answerTurns(s, method, params)
			},
		}
		subject := newProvider(t, server, Config{})

		res, err := subject.Run(ctx, provider.Request{Session: "thread-1", Prompt: "long task", OnDelta: func(string) { cancel() }})
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 2, server.Starts(), "restarted as soon as no turn was running")

		res, err = subject.Run(context.Background(), provider.Request{Session: res.Session, Prompt: "again"})

		require.NoError(t, err)
		assert.Equal(t, "done", res.Text)
		assert.Equal(t, []string{"initialize", "thread/resume", "turn/start", "turn/interrupt",
			"initialize", "thread/resume", "turn/start"}, server.Calls())
	})

	t.Run("Too many loaded threads restarts the app-server", func(t *testing.T) {
		server := &fakeAppServer{Handle: answerTurns}
		subject := newProvider(t, server, Config{MaxLoadedThreads: 2})

		for _, session := range []string{"a", "b"} {
			_, err := subject.Run(context.Background(), provider.Request{Session: session, Prompt: "hi"})
			require.NoError(t, err)
		}
		assert.Equal(t, 1, server.Starts())

		_, err := subject.Run(context.Background(), provider.Request{Session: "c", Prompt: "hi"})

		require.NoError(t, err)
		assert.Equal(t, 2, server.Starts())
	})

	t.Run("Idle app-server is restarted", func(t *testing.T) {
		server := &fakeAppServer{Handle: answerTurns}
		subject := newProvider(t, server, Config{IdleRestart: 20 * time.Millisecond})

		_, err := subject.Run(context.Background(), provider.Request{Session: "a", Prompt: "hi"})

		require.NoError(t, err)
		assert.Eventually(t, func() bool { return server.Starts() == 2 }, eventually, tick)
		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, 2, server.Starts(), "no restart while nothing is loaded")
	})

	t.Run("No restart while another turn is running", func(t *testing.T) {
		release := make(chan struct{})
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				var p turnStartParams
				_ = json.Unmarshal(params, &p)
				if method == "turn/start" && p.ThreadID == "slow" {
					go func() {
						<-release
						s.Notify("turn/completed", completed("slow", "turn-1", "completed", nil))
					}()
					return turnResult("turn-1"), nil
				}
				return answerTurns(s, method, params)
			},
		}
		subject := newProvider(t, server, Config{MaxLoadedThreads: 1})
		slow := make(chan error, 1)
		go func() {
			_, err := subject.Run(context.Background(), provider.Request{Session: "slow", Prompt: "hi"})
			slow <- err
		}()
		assert.Eventually(t, func() bool { return subject.turn("slow") != nil }, eventually, tick)

		_, err := subject.Run(context.Background(), provider.Request{Session: "fast", Prompt: "hi"})
		require.NoError(t, err)
		assert.Equal(t, 1, server.Starts(), "the slow turn is still running")

		close(release)
		require.NoError(t, <-slow)
		assert.Equal(t, 2, server.Starts(), "restarted once the last turn finished")
	})
}

func TestTurnErrorIs(t *testing.T) {
	t.Run("Fallback codes match ErrUnavailable", func(t *testing.T) {
		for _, code := range []string{
			"usageLimitExceeded", "rateLimitExceeded", "serverOverloaded", "internalServerError",
			"unauthorized", "httpConnectionFailed", "responseStreamConnectionFailed",
			"responseStreamDisconnected", "responseTooManyFailedAttempts",
		} {
			assert.True(t, errors.Is(&TurnError{Code: code}, provider.ErrUnavailable), code)
		}
	})

	t.Run("Other codes do not match", func(t *testing.T) {
		for _, code := range []string{"", "badRequest", "contextWindowExceeded", "cyberPolicy", "sandboxError", "other"} {
			assert.False(t, errors.Is(&TurnError{Code: code}, provider.ErrUnavailable), code)
		}
	})
}
