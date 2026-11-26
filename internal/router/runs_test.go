package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActiveRuns(t *testing.T) {
	subject := (*Router).ActiveRuns

	t.Run("Tracks queued, running, progress, and completion across concurrent turns", func(t *testing.T) {
		entered := make(chan string, 2)
		release := make(chan struct{})
		now := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)
		var nowMu sync.Mutex
		clock := func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			now = now.Add(3 * time.Second)
			return now
		}

		var snapsMu sync.Mutex
		var snaps [][]ActiveRun
		r := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Now:    clock,
			OnRunsChanged: func(runs []ActiveRun) {
				snapsMu.Lock()
				snaps = append(snaps, runs)
				snapsMu.Unlock()
			},
			Run: func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
				if req.OnProgress != nil {
					req.OnProgress("Tool activity observed")
				}
				entered <- req.Prompt
				select {
				case <-release:
				case <-ctx.Done():
					return "", provider.Result{}, ctx.Err()
				}
				return "codex", provider.Result{Text: "done"}, nil
			},
		})

		require.True(t, r.Submit(Message{
			ChatKey:  "job:assistant:code-review",
			Agent:    "assistant",
			RunKind:  "ask",
			RunLabel: "code-review",
			Text:     "first",
			Reply:    func(context.Context, string) error { return nil },
		}))
		assert.Equal(t, "first", <-entered)

		// Second message on the same chat queues behind the first.
		require.True(t, r.Submit(Message{
			ChatKey:  "job:assistant:code-review",
			Agent:    "assistant",
			RunKind:  "handoff",
			RunLabel: "follow-on",
			TaskID:   "task-2",
			Text:     "second",
			Reply:    func(context.Context, string) error { return nil },
		}))

		runs := subject(r)
		require.Len(t, runs, 2)
		assert.Equal(t, "running", runs[0].State)
		assert.Equal(t, "assistant", runs[0].Agent)
		assert.Equal(t, "ask", runs[0].Kind)
		assert.Equal(t, "code-review", runs[0].Label)
		assert.Equal(t, "Tool activity observed", runs[0].Progress)

		assert.Equal(t, "queued", runs[1].State)
		assert.Equal(t, "handoff", runs[1].Kind)
		assert.Equal(t, "task-2", runs[1].ID)
		assert.Equal(t, "task-2", runs[1].TaskID)

		close(release)
		assert.Equal(t, "second", <-entered)
		require.Eventually(t, func() bool { return len(subject(r)) == 0 }, time.Second, 5*time.Millisecond)

		snapsMu.Lock()
		last := snaps[len(snaps)-1]
		snapsMu.Unlock()
		assert.Empty(t, last)
	})

	t.Run("Ignores built-in slash commands", func(t *testing.T) {
		var notified bool
		r := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			OnRunsChanged: func([]ActiveRun) {
				notified = true
			},
		})
		done := make(chan struct{})
		require.True(t, r.Submit(Message{
			ChatKey: "app:assistant",
			Agent:   "assistant",
			Text:    "/new",
			Reply:   func(context.Context, string) error { return nil },
			OnDone:  func(TurnResult) { close(done) },
		}))
		<-done
		assert.False(t, notified)
		assert.Empty(t, subject(r))
	})

	t.Run("Removes absorbed queued runs when messages are batched", func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		secondDone := make(chan struct{})
		r := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
				entered <- struct{}{}
				if req.Prompt == "first" {
					<-release
				}
				return "codex", provider.Result{Text: "ok"}, nil
			},
		})
		require.True(t, r.Submit(Message{
			ChatKey: "app:assistant",
			Agent:   "assistant",
			Batch:   true,
			Text:    "first",
			Reply:   func(context.Context, string) error { return nil },
		}))
		<-entered
		require.True(t, r.Submit(Message{
			ChatKey: "app:assistant",
			Agent:   "assistant",
			Batch:   true,
			Text:    "second",
			Reply:   func(context.Context, string) error { return nil },
		}))
		require.True(t, r.Submit(Message{
			ChatKey: "app:assistant",
			Agent:   "assistant",
			Batch:   true,
			Text:    "third",
			Reply:   func(context.Context, string) error { return nil },
			OnDone:  func(TurnResult) { close(secondDone) },
		}))
		assert.Len(t, subject(r), 3)
		close(release)
		<-entered
		<-secondDone
		require.Eventually(t, func() bool { return len(subject(r)) == 0 }, time.Second, 5*time.Millisecond)
	})

	t.Run("Full queue does not leak a run or notify", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var mu sync.Mutex
		var notifyCount int
		r := newRouter(t, Config{
			Agents:    testAgents(),
			Store:     store.NewTestStore(t),
			QueueSize: 1,
			OnRunsChanged: func([]ActiveRun) {
				mu.Lock()
				notifyCount++
				mu.Unlock()
			},
			Run: func(ctx context.Context, _ provider.Sessions, _ provider.Request) (string, provider.Result, error) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return "codex", provider.Result{Text: "ok"}, nil
			},
		})
		require.True(t, r.Submit(Message{ChatKey: "job:assistant:q", Agent: "assistant", Text: "1", Reply: func(context.Context, string) error { return nil }}))
		<-entered
		require.True(t, r.Submit(Message{ChatKey: "job:assistant:q", Agent: "assistant", Text: "2", Reply: func(context.Context, string) error { return nil }}))
		mu.Lock()
		before := notifyCount
		mu.Unlock()
		assert.False(t, r.Submit(Message{ChatKey: "job:assistant:q", Agent: "assistant", Text: "3", Reply: func(context.Context, string) error { return nil }}))
		assert.Len(t, subject(r), 2)
		mu.Lock()
		assert.Equal(t, before, notifyCount)
		mu.Unlock()
		close(release)
		require.Eventually(t, func() bool { return len(subject(r)) == 0 }, time.Second, 5*time.Millisecond)
	})

	t.Run("OnDone sees the finished run already cleared and shutdown drains queued runs", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		release := make(chan struct{})
		var activeAtOnDone int
		done := make(chan struct{})
		r := New(ctx, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
				if req.Prompt == "sync" {
					return "codex", provider.Result{Text: "done"}, nil
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return "", provider.Result{}, ctx.Err()
				}
				return "codex", provider.Result{Text: "done"}, nil
			},
		})
		require.True(t, r.Submit(Message{
			ChatKey: "job:assistant:sync",
			Agent:   "assistant",
			Text:    "sync",
			Reply:   func(context.Context, string) error { return nil },
			OnDone: func(TurnResult) {
				activeAtOnDone = len(subject(r))
				close(done)
			},
		}))
		<-done
		assert.Equal(t, 0, activeAtOnDone, "finishRun runs before OnDone")

		require.True(t, r.Submit(Message{ChatKey: "job:assistant:drain", Agent: "assistant", Text: "first", Reply: func(context.Context, string) error { return nil }}))
		<-entered
		require.True(t, r.Submit(Message{ChatKey: "job:assistant:drain", Agent: "assistant", Text: "queued", Reply: func(context.Context, string) error { return nil }}))
		assert.Len(t, subject(r), 2)
		cancel()
		r.Wait()
		assert.Empty(t, subject(r), "shutdown drains queued runs")
		assert.False(t, r.Submit(Message{ChatKey: "job:assistant:drain", Agent: "assistant", Text: "late", Reply: func(context.Context, string) error { return nil }}))
		assert.Empty(t, subject(r))
		close(release)
	})
}

func TestPinnedReadOnlyTurns(t *testing.T) {
	subject := (*Router).Submit

	t.Run("Applies read-only settings, pinned provider, and custom instructions", func(t *testing.T) {
		var got provider.Request
		done := make(chan TurnResult, 1)
		r := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
				got = req
				return "claude", provider.Result{Text: "LGTM"}, nil
			},
		})
		require.True(t, subject(r, Message{
			ChatKey:      "job:assistant:review:pr-1",
			Agent:        "assistant",
			RunKind:      "review",
			RunLabel:     "claude: pr-1",
			Provider:     "claude",
			ReadOnly:     true,
			Instructions: "Independent reviewer only.",
			Text:         "Review commit abc",
			Reply:        func(context.Context, string) error { return nil },
			OnDone:       func(res TurnResult) { done <- res },
		}))
		res := <-done
		require.NoError(t, res.Err)
		assert.Equal(t, "claude", res.Provider)
		assert.Equal(t, "LGTM", res.Text)
		assert.Equal(t, "claude", got.Provider)
		assert.Equal(t, "Independent reviewer only.", got.Instructions)
		assert.Equal(t, "read-only", got.Settings.CodexSandbox)
		assert.Equal(t, "plan", got.Settings.PermissionMode)
		assert.Equal(t, "plan", got.Settings.AntigravityMode)
		assert.True(t, got.Settings.NoMCP)
	})

	t.Run("Reports pinned provider unavailability accurately", func(t *testing.T) {
		done := make(chan TurnResult, 1)
		r := newRouter(t, Config{
			Agents: testAgents(),
			Store:  store.NewTestStore(t),
			Run: func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
				return "", provider.Result{}, provider.ErrUnavailable
			},
		})
		require.True(t, subject(r, Message{
			ChatKey:  "job:assistant:review:pr-2",
			Agent:    "assistant",
			Provider: "claude",
			ReadOnly: true,
			Text:     "Review commit def",
			Reply:    func(context.Context, string) error { return nil },
			OnDone:   func(res TurnResult) { done <- res },
		}))
		res := <-done
		require.ErrorIs(t, res.Err, provider.ErrUnavailable)
		assert.Contains(t, res.Text, "Provider claude is unavailable right now.")
	})
}
