package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
)

const waitFor = 2 * time.Second

func testAgents() map[string]agent.Agent {
	return map[string]agent.Agent{
		"assistant": {
			Name:             "assistant",
			Workspace:        "/ws/assistant",
			InstructionFiles: []string{"SOUL.md"},
			ReadFile: func(path string) ([]byte, error) {
				return []byte("Be helpful."), nil
			},
		},
	}
}

// replies collects Reply calls for a chat.
type replies struct {
	ch chan string
}

func newReplies() *replies { return &replies{ch: make(chan string, 16)} }

func (r *replies) msg(chat, text string) Message {
	return Message{
		ChatKey: chat,
		Agent:   "assistant",
		Text:    text,
		Reply: func(ctx context.Context, s string) error {
			r.ch <- s
			return nil
		},
	}
}

func (r *replies) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-r.ch:
		return s
	case <-time.After(waitFor):
		t.Fatal("timed out waiting for reply")
		return ""
	}
}

func newRouter(t *testing.T, cfg Config) *Router {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := New(ctx, cfg)
	t.Cleanup(func() { cancel(); r.Wait() })
	return r
}

func TestRouterSubmit(t *testing.T) {
	ctx := context.Background()

	t.Run("SuccessFlow", func(t *testing.T) {
		db := store.NewTestStore(t)
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  db,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Equal(t, "/ws/assistant", req.Cwd)
				assert.Equal(t, "# SOUL.md\n\nBe helpful.", req.Instructions)
				assert.Equal(t, "hello", req.Prompt)
				assert.Empty(t, sessions)
				sessions["codex"] = "c-1"
				return "codex", provider.Result{Session: "c-1", Text: "hi there"}, nil
			},
		})

		assert.True(t, subject.Submit(out.msg("telegram:bot:1", "hello")))

		assert.Equal(t, "hi there", out.next(t))
		saved, err := db.Sessions(ctx, "telegram:bot:1")
		require.NoError(t, err)
		assert.Equal(t, provider.Sessions{"codex": "c-1"}, saved)
	})

	t.Run("Agent settings reach the provider", func(t *testing.T) {
		out := newReplies()
		agents := testAgents()
		a := agents["assistant"]
		a.Settings = provider.Settings{CodexModel: "m1", PermissionMode: "bypassPermissions"}
		agents["assistant"] = a
		subject := newRouter(t, Config{
			Agents: agents,
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Equal(t, provider.Settings{CodexModel: "m1", PermissionMode: "bypassPermissions"}, req.Settings)
				return "codex", provider.Result{Text: "ok"}, nil
			},
		})

		subject.Submit(out.msg("chat", "hi"))

		assert.Equal(t, "ok", out.next(t))
	})

	t.Run("Stored sessions are passed back in", func(t *testing.T) {
		db := store.NewTestStore(t)
		require.NoError(t, db.SaveSessions(ctx, "telegram:bot:1", provider.Sessions{"codex": "c-1", "claude": "k-1"}))
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  db,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Equal(t, provider.Sessions{"codex": "c-1", "claude": "k-1"}, sessions)
				return "codex", provider.Result{Text: "welcome back"}, nil
			},
		})

		subject.Submit(out.msg("telegram:bot:1", "again"))

		assert.Equal(t, "welcome back", out.next(t))
	})

	for _, cmd := range []string{" /new ", "/reset"} {
		t.Run(cmd+" resets the conversation without running the agent", func(t *testing.T) {
			db := store.NewTestStore(t)
			require.NoError(t, db.SaveSessions(ctx, "telegram:bot:1", provider.Sessions{"codex": "c-1", "claude": "cl-1"}))
			require.NoError(t, db.SaveSessions(ctx, "telegram:bot:2", provider.Sessions{"codex": "c-2"}))
			out := newReplies()
			subject := newRouter(t, Config{
				Agents: testAgents(),
				Store:  db,
				Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
					t.Error("agent must not run for " + cmd)
					return "", provider.Result{}, nil
				},
			})

			subject.Submit(out.msg("telegram:bot:1", cmd))

			assert.Equal(t, "Started a new conversation.", out.next(t))
			s1, _ := db.Sessions(ctx, "telegram:bot:1")
			s2, _ := db.Sessions(ctx, "telegram:bot:2")
			assert.Empty(t, s1)
			assert.Equal(t, provider.Sessions{"codex": "c-2"}, s2)
		})
	}

	t.Run("Messages in one chat run one at a time, in order", func(t *testing.T) {
		db := store.NewTestStore(t)
		out := newReplies()
		var active, maxActive atomic.Int32
		release := make(chan struct{})
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  db,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				n := active.Add(1)
				defer active.Add(-1)
				if n > maxActive.Load() {
					maxActive.Store(n)
				}
				if req.Prompt == "first" {
					<-release
				}
				return "codex", provider.Result{Text: "re: " + req.Prompt}, nil
			},
		})

		subject.Submit(out.msg("chat", "first"))
		subject.Submit(out.msg("chat", "second"))
		subject.Submit(out.msg("chat", "third"))
		close(release)

		assert.Equal(t, "re: first", out.next(t))
		assert.Equal(t, "re: second", out.next(t))
		assert.Equal(t, "re: third", out.next(t))
		assert.Equal(t, int32(1), maxActive.Load())
	})

	t.Run("Different chats run concurrently", func(t *testing.T) {
		db := store.NewTestStore(t)
		out := newReplies()
		bStarted := make(chan struct{})
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  db,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				if req.Prompt == "a" {
					select {
					case <-bStarted: // only returns if chat b ran while a was busy
					case <-time.After(waitFor):
						return "", provider.Result{}, errors.New("chat b never started")
					}
				} else {
					close(bStarted)
				}
				return "codex", provider.Result{Text: "done " + req.Prompt}, nil
			},
		})

		subject.Submit(out.msg("chat-a", "a"))
		subject.Submit(out.msg("chat-b", "b"))

		got := []string{out.next(t), out.next(t)}
		assert.ElementsMatch(t, []string{"done a", "done b"}, got)
	})

	t.Run("Unavailable providers give a friendly reply and still save sessions", func(t *testing.T) {
		db := store.NewTestStore(t)
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  db,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				sessions["codex"] = "c-created"
				return "", provider.Result{}, errors.Join(provider.ErrUnavailable, errors.New("usage limit"))
			},
		})

		subject.Submit(out.msg("chat", "hello"))

		reply := out.next(t)
		assert.Contains(t, reply, "All models are unavailable right now.")
		assert.Contains(t, reply, "usage limit")
		saved, _ := db.Sessions(ctx, "chat")
		assert.Equal(t, provider.Sessions{"codex": "c-created"}, saved)
	})

	t.Run("Other errors are reported", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "", provider.Result{}, errors.New("bad request")
			},
		})

		subject.Submit(out.msg("chat", "hello"))

		assert.Equal(t, "Something went wrong: bad request", out.next(t))
	})

	t.Run("Turns that exceed the timeout are stopped", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents:      testAgents(),
			Store:       store.NewTestStore(t),
			TurnTimeout: 30 * time.Millisecond,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				<-ctx.Done()
				return "", provider.Result{}, ctx.Err()
			},
		})

		subject.Submit(out.msg("chat", "slow"))

		assert.Equal(t, "That took longer than 30ms and was stopped.", out.next(t))
	})

	t.Run("Unknown agent is reported", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{Agents: testAgents(), Store: store.NewTestStore(t)})
		m := out.msg("chat", "hello")
		m.Agent = "ghost"

		subject.Submit(m)

		assert.Equal(t, "This bot isn't connected to an agent.", out.next(t))
	})

	t.Run("Empty reply is replaced with a placeholder", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "claude", provider.Result{Text: ""}, nil
			},
		})

		subject.Submit(out.msg("chat", "hello"))

		assert.Equal(t, "(no reply)", out.next(t))
	})

	t.Run("Typing indicator repeats while the agent works, then stops", func(t *testing.T) {
		out := newReplies()
		var typing atomic.Int32
		twice := make(chan struct{})
		var once sync.Once
		subject := newRouter(t, Config{
			Agents:         testAgents(),
			Store:          store.NewTestStore(t),
			TypingInterval: 5 * time.Millisecond,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				<-twice
				return "codex", provider.Result{Text: "done"}, nil
			},
		})
		m := out.msg("chat", "hello")
		m.Typing = func(ctx context.Context) error {
			if typing.Add(1) >= 2 {
				once.Do(func() { close(twice) })
			}
			return nil
		}

		subject.Submit(m)

		assert.Equal(t, "done", out.next(t))
		after := typing.Load()
		time.Sleep(30 * time.Millisecond)
		assert.Equal(t, after, typing.Load(), "typing must stop after the reply")
	})

	t.Run("Full queue rejects new messages", func(t *testing.T) {
		out := newReplies()
		release := make(chan struct{})
		started := make(chan struct{}, 1)
		subject := newRouter(t, Config{
			Agents:    testAgents(),
			Store:     store.NewTestStore(t),
			QueueSize: 1,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				started <- struct{}{}
				<-release
				return "codex", provider.Result{Text: req.Prompt}, nil
			},
		})

		assert.True(t, subject.Submit(out.msg("chat", "1")))
		<-started
		assert.True(t, subject.Submit(out.msg("chat", "2")), "one message may wait")
		assert.False(t, subject.Submit(out.msg("chat", "3")), "queue is full")
		close(release)

		assert.Equal(t, "1", out.next(t))
		<-started
		assert.Equal(t, "2", out.next(t))
	})

	t.Run("Idle chat workers exit and restart on the next message", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents:      testAgents(),
			Store:       store.NewTestStore(t),
			IdleTimeout: 10 * time.Millisecond,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "codex", provider.Result{Text: req.Prompt}, nil
			},
		})

		subject.Submit(out.msg("chat", "one"))
		assert.Equal(t, "one", out.next(t))
		assert.Eventually(t, func() bool {
			subject.mu.Lock()
			defer subject.mu.Unlock()
			return len(subject.queues) == 0
		}, waitFor, 5*time.Millisecond)

		subject.Submit(out.msg("chat", "two"))
		assert.Equal(t, "two", out.next(t))
	})

	t.Run("/status replies with provider status", func(t *testing.T) {
		out := newReplies()
		now := time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Now:    func() time.Time { return now },
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				t.Error("agent must not run for /status")
				return "", provider.Result{}, nil
			},
			Status: func(ctx context.Context) []provider.Status {
				return []provider.Status{
					{Provider: "codex", Known: true, Reason: "usage limit reached", ResetsAt: now.Add(2 * time.Hour)},
					{Provider: "claude", Known: true, Available: true},
				}
			},
		})

		subject.Submit(out.msg("chat", "/status"))

		assert.Equal(t, "codex: unavailable — usage limit reached (resets Sep 30 06:00 UTC, in 2h 0m)\n\nclaude: available", out.next(t))
	})

	t.Run("/status without a status source", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{Agents: testAgents(), Store: store.NewTestStore(t)})

		subject.Submit(out.msg("chat", "/status"))

		assert.Equal(t, "Status isn't available.", out.next(t))
	})

	t.Run("Streams deltas and finishes with the final text", func(t *testing.T) {
		var mu sync.Mutex
		var events []string
		record := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
		finished := make(chan string, 1)
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				req.OnDelta("par")
				req.OnReset()
				req.OnDelta("Hel")
				req.OnDelta("lo")
				return "claude", provider.Result{Text: "Hello"}, nil
			},
		})
		m := newReplies().msg("chat", "hi")
		m.Reply = func(ctx context.Context, s string) error {
			t.Error("Reply must not be used when streaming")
			return nil
		}
		m.Stream = func(ctx context.Context) *ReplyStream {
			return &ReplyStream{
				Append: func(s string) { record("append:" + s) },
				Reset:  func() { record("reset") },
				Finish: func(ctx context.Context, final string) error { finished <- final; return nil },
			}
		}

		subject.Submit(m)

		assert.Equal(t, "Hello", <-finished)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"append:par", "reset", "append:Hel", "append:lo"}, events)
	})

	t.Run("Errors finish the stream", func(t *testing.T) {
		finished := make(chan string, 1)
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				req.OnDelta("partial")
				return "", provider.Result{}, errors.New("boom")
			},
		})
		m := newReplies().msg("chat", "hi")
		m.Stream = func(ctx context.Context) *ReplyStream {
			return &ReplyStream{Append: func(string) {}, Reset: func() {},
				Finish: func(ctx context.Context, final string) error { finished <- final; return nil }}
		}

		subject.Submit(m)

		assert.Equal(t, "Something went wrong: boom", <-finished)
	})

	t.Run("/stop cancels the running turn", func(t *testing.T) {
		out := newReplies()
		started := make(chan struct{})
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				close(started)
				<-ctx.Done()
				return "", provider.Result{}, ctx.Err()
			},
		})

		subject.Submit(out.msg("chat", "long task"))
		<-started
		assert.True(t, subject.Submit(out.msg("chat", "/stop")))

		assert.Equal(t, "Stopped.", out.next(t))
	})

	t.Run("/stop only affects its own chat", func(t *testing.T) {
		a, b := newReplies(), newReplies()
		started := make(chan struct{})
		release := make(chan struct{})
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				close(started)
				select {
				case <-release:
					return "codex", provider.Result{Text: "finished"}, nil
				case <-ctx.Done():
					return "", provider.Result{}, ctx.Err()
				}
			},
		})

		subject.Submit(a.msg("chat-a", "work"))
		<-started
		subject.Submit(b.msg("chat-b", "/stop"))
		assert.Equal(t, "Nothing is running.", b.next(t))
		close(release)

		assert.Equal(t, "finished", a.next(t))
	})

	t.Run("/help and /start list the commands", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{Agents: testAgents(), Store: store.NewTestStore(t)})

		subject.Submit(out.msg("chat", "/help"))
		subject.Submit(out.msg("chat", "/start"))

		help := out.next(t)
		assert.Contains(t, help, "/new — Start a new conversation")
		assert.Contains(t, help, "/reset — Clear the conversation (same as /new)")
		assert.Contains(t, help, "/stop — Stop the reply in progress")
		assert.Equal(t, help, out.next(t))
	})

	t.Run("Attachments are fetched into the workspace and described to the agent", func(t *testing.T) {
		out := newReplies()
		img := provider.Attachment{Path: "/ws/assistant/.ganclaw/attachments/x-photo.jpg", Name: "photo.jpg", MimeType: "image/jpeg", Kind: provider.KindImage, Size: 2048}
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Equal(t, []provider.Attachment{img}, req.Attachments)
				assert.Equal(t, "what is this?\n\nThe user attached these files (saved in your workspace):\n"+
					"- image: /ws/assistant/.ganclaw/attachments/x-photo.jpg (photo.jpg, image/jpeg, 2 KB)", req.Prompt)
				return "codex", provider.Result{Text: "a pelican"}, nil
			},
		})
		m := out.msg("chat", "what is this?")
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			assert.Equal(t, "/ws/assistant/.ganclaw/attachments", dir)
			return []provider.Attachment{img}, nil
		}

		subject.Submit(m)

		assert.Equal(t, "a pelican", out.next(t))
	})

	t.Run("A caption that looks like a command is sent to the agent", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "codex", provider.Result{Text: "handled by agent"}, nil
			},
		})
		m := out.msg("chat", "/new")
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) { return nil, nil }

		subject.Submit(m)

		assert.Equal(t, "handled by agent", out.next(t))
	})

	t.Run("Fetch errors are reported without running the agent", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				t.Error("agent must not run")
				return "", provider.Result{}, nil
			},
		})
		m := out.msg("chat", "")
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			return nil, errors.New("movie.mp4 is 25.0 MB; Telegram only lets bots download files up to 20 MB")
		}

		subject.Submit(m)

		assert.Equal(t, "Couldn't get the attachment: movie.mp4 is 25.0 MB; Telegram only lets bots download files up to 20 MB", out.next(t))
	})

	t.Run("Voice notes are transcribed into the prompt", func(t *testing.T) {
		out := newReplies()
		voice := provider.Attachment{Path: "/ws/v.ogg", Name: "voice.ogg", MimeType: "audio/ogg", Kind: provider.KindAudio, Size: 30 << 10}
		photo := provider.Attachment{Path: "/ws/p.jpg", Name: "photo.jpg", Kind: provider.KindImage, Size: 10}
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Transcribe: func(ctx context.Context, path string) (string, error) {
				assert.Equal(t, "/ws/v.ogg", path, "only audio is transcribed")
				return "remind me to call the bank", nil
			},
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Equal(t, "remind me to call the bank", req.Attachments[0].Transcript)
				assert.Contains(t, req.Prompt, "- audio: /ws/v.ogg (voice.ogg, audio/ogg, 30 KB)\n  Transcript: remind me to call the bank")
				return "codex", provider.Result{Text: "noted"}, nil
			},
		})
		m := out.msg("chat", "")
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			return []provider.Attachment{voice, photo}, nil
		}

		subject.Submit(m)

		assert.Equal(t, "noted", out.next(t))
	})

	t.Run("Transcription failure still runs the agent", func(t *testing.T) {
		out := newReplies()
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Transcribe: func(ctx context.Context, path string) (string, error) {
				return "", errors.New("whisper: model not found")
			},
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				assert.Contains(t, req.Prompt, "(Transcription failed: whisper: model not found)")
				return "codex", provider.Result{Text: "I can't hear that one"}, nil
			},
		})
		m := out.msg("chat", "")
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			return []provider.Attachment{{Path: "/ws/v.ogg", Name: "voice.ogg", Kind: provider.KindAudio}}, nil
		}

		subject.Submit(m)

		assert.Equal(t, "I can't hear that one", out.next(t))
	})

	t.Run("Typing shows while attachments download", func(t *testing.T) {
		out := newReplies()
		typed := make(chan struct{}, 1)
		subject := newRouter(t, Config{
			Agents:         testAgents(),
			Store:          store.NewTestStore(t),
			TypingInterval: time.Hour,
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "codex", provider.Result{Text: "done"}, nil
			},
		})
		m := out.msg("chat", "look")
		m.Typing = func(ctx context.Context) error {
			select {
			case typed <- struct{}{}:
			default:
			}
			return nil
		}
		m.Fetch = func(ctx context.Context, dir string) ([]provider.Attachment, error) {
			select {
			case <-typed:
			case <-time.After(waitFor):
				t.Error("typing indicator not shown during download")
			}
			return nil, nil
		}

		subject.Submit(m)

		assert.Equal(t, "done", out.next(t))
	})

	t.Run("OnDone reports the outcome", func(t *testing.T) {
		done := make(chan TurnResult, 3)
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				if req.Prompt == "fail" {
					return "", provider.Result{}, errors.Join(provider.ErrUnavailable, errors.New("limits"))
				}
				return "codex", provider.Result{Text: "fine"}, nil
			},
		})
		for _, text := range []string{"ok", "fail", "/new"} {
			m := newReplies().msg("chat", text)
			m.OnDone = func(r TurnResult) { done <- r }
			subject.Submit(m)
		}

		ok, fail, cmd := <-done, <-done, <-done
		assert.Equal(t, TurnResult{Provider: "codex", Text: "fine"}, ok)
		assert.ErrorIs(t, fail.Err, provider.ErrUnavailable)
		assert.Contains(t, fail.Text, "All models are unavailable")
		assert.Equal(t, TurnResult{Text: "Started a new conversation."}, cmd)
	})

	t.Run("Message Ctx cancels the turn", func(t *testing.T) {
		done := make(chan TurnResult, 1)
		started := make(chan struct{})
		subject := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
				close(started)
				<-ctx.Done()
				return "", provider.Result{}, ctx.Err()
			},
		})
		mctx, cancel := context.WithCancel(context.Background())
		m := newReplies().msg("chat", "long")
		m.Ctx = mctx
		m.OnDone = func(r TurnResult) { done <- r }

		subject.Submit(m)
		<-started
		cancel()

		assert.ErrorIs(t, (<-done).Err, context.Canceled)
	})

	t.Run("Submit after shutdown is rejected", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		subject := New(ctx, Config{Agents: testAgents(), Store: store.NewTestStore(t)})
		cancel()
		subject.Wait()

		assert.False(t, subject.Submit(newReplies().msg("chat", "late")))
	})
}

func TestCommand(t *testing.T) {
	subject := command

	t.Run("Parses slash commands", func(t *testing.T) {
		assert.Equal(t, "status", subject("/status"))
		assert.Equal(t, "status", subject("  /Status@MyBot  "))
		assert.Equal(t, "new", subject("/new"))
	})

	t.Run("Anything else is not a command", func(t *testing.T) {
		assert.Equal(t, "", subject("status"))
		assert.Equal(t, "", subject("/new please summarise my inbox"), "commands with text go to the agent")
		assert.Equal(t, "", subject("/usr/bin/env\nls"))
		assert.Equal(t, "", subject(""))
	})
}

func TestBuildPrompt(t *testing.T) {
	subject := BuildPrompt

	t.Run("No attachments leaves the text alone", func(t *testing.T) {
		assert.Equal(t, "hello", subject("hello", nil))
	})

	t.Run("Attachment-only message", func(t *testing.T) {
		got := subject("  ", []provider.Attachment{
			{Path: "/a/voice.ogg", Name: "voice.ogg", MimeType: "audio/ogg", Kind: provider.KindAudio, Size: 3 << 20},
			{Path: "/a/notes", Name: "notes", Kind: provider.KindFile, Size: 12},
		})

		assert.Equal(t, "The user attached these files (saved in your workspace):\n"+
			"- audio: /a/voice.ogg (voice.ogg, audio/ogg, 3.0 MB)\n"+
			"- file: /a/notes (notes, 12 bytes)", got)
	})
}
