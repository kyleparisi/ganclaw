package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/appchannel"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerTasks(t *testing.T) {
	subject := newGateway
	t.Run("Distinguishes queued work from running and records completion failures", func(t *testing.T) {
		ctx := context.Background()
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		defer close(release)
		g, db := subject(t, &sentMessages{}, func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
			req.OnProgress("Tool activity observed")
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return "", provider.Result{}, errors.New("private failure detail")
		})
		first, err := g.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "private prompt", Async: true, Session: "work"}, nil)
		require.NoError(t, err)
		require.NotEmpty(t, first.TaskID)
		<-entered
		second, err := g.Run(ctx, api.RunRequest{Agent: "assistant", Prompt: "another prompt", Async: true, Session: "work"}, nil)
		require.NoError(t, err)
		q, err := db.Task(ctx, second.TaskID)
		require.NoError(t, err)
		assert.Equal(t, "queued", q.State)
		assert.Nil(t, q.StartedAt)
		r, err := db.Task(ctx, first.TaskID)
		require.NoError(t, err)
		assert.Equal(t, "running", r.State)
		assert.Equal(t, "Tool activity observed", r.Progress)
		release <- struct{}{}
		require.Eventually(t, func() bool { task, err := db.Task(ctx, first.TaskID); return err == nil && task.State == "failed" }, time.Second, time.Millisecond)
		failed, err := db.Task(ctx, first.TaskID)
		require.NoError(t, err)
		assert.NotContains(t, failed.Progress, "private")
	})
	t.Run("Rejected queue submissions never report running", func(t *testing.T) {
		g, db := subject(t, &sentMessages{}, func(context.Context, provider.Sessions, provider.Request) (string, provider.Result, error) {
			return "", provider.Result{}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		g.Router = router.New(ctx, router.Config{Store: db})
		_, err := g.Run(context.Background(), api.RunRequest{Agent: "support", Prompt: "task", Async: true}, nil)
		require.Error(t, err)
		tasks, err := db.RecentTasks(context.Background(), "support")
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		failed := tasks[0]
		assert.Equal(t, "failed", failed.State)
		assert.Nil(t, failed.StartedAt)
	})
}

func TestAppWorkerResults(t *testing.T) {
	subject := newGateway
	t.Run("Delivers to original app thread and hidden task history without job or typing leakage", func(t *testing.T) {
		ctx := context.Background()
		entered, release := make(chan struct{}), make(chan struct{})
		g, db := subject(t, &sentMessages{}, func(context.Context, provider.Sessions, provider.Request) (string, provider.Result, error) {
			close(entered)
			<-release
			return "fake", provider.Result{Text: "Work finished"}, nil
		})
		g.App = &appchannel.Channel{Agents: g.Agents, Store: db, FilesDir: t.TempDir()}
		// Initialize the channel and provide an ordinary inbox message.
		_, err := g.App.Post(ctx, "support", "user", "Start", "")
		require.NoError(t, err)
		res, err := g.Run(ctx, api.RunRequest{Agent: "support", Prompt: "work", Async: true, Session: "worker-session"}, &api.Target{Channel: "app", Bot: "support"})
		require.NoError(t, err)
		<-entered
		close(release)
		require.Eventually(t, func() bool { task, err := db.Task(ctx, res.TaskID); return err == nil && task.State == "completed" }, time.Second, time.Millisecond)
		history, err := db.AppMessages(ctx, "task:"+res.TaskID, 0, 100)
		require.NoError(t, err)
		require.Len(t, history, 1)
		assert.Equal(t, "Work finished", history[0].Text)
		original, err := db.AppMessages(ctx, "support", 0, 100)
		require.NoError(t, err)
		require.Len(t, original, 2)
		assert.Equal(t, "Work finished", original[1].Text)
		assert.Empty(t, original[1].Job)
		latest, err := db.LastAppMessages(ctx)
		require.NoError(t, err)
		require.Len(t, latest, 1)
		assert.Equal(t, "support", latest["support"].Thread)
	})
}

func TestUnnamedWorkerSessions(t *testing.T) {
	subject := newGateway
	t.Run("Concurrent unnamed workers do not enter the ordinary app conversation", func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		defer close(release)
		g, db := subject(t, &sentMessages{}, func(ctx context.Context, _ provider.Sessions, _ provider.Request) (string, provider.Result, error) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return "fake", provider.Result{Text: "Done"}, nil
		})
		g.App = &appchannel.Channel{Agents: g.Agents, Store: db, FilesDir: t.TempDir()}
		for i := 0; i < 2; i++ {
			_, err := g.Run(context.Background(), api.RunRequest{Agent: "support", Prompt: "work", Async: true}, &api.Target{Channel: "app", Bot: "support"})
			require.NoError(t, err)
		}
		for i := 0; i < 2; i++ {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("unnamed workers shared a conversation queue")
			}
		}
	})
}

func TestActiveRunMetadata(t *testing.T) {
	subject := (*Gateway).Run

	t.Run("Tracks ask_agent, hand_off, and quiet follow-ups in router ActiveRuns", func(t *testing.T) {
		ctx := context.Background()
		entered := make(chan struct{}, 3)
		release := make(chan struct{})
		g, db := newGateway(t, &sentMessages{}, func(ctx context.Context, _ provider.Sessions, req provider.Request) (string, provider.Result, error) {
			if req.OnProgress != nil {
				req.OnProgress("Tool activity observed")
			}
			entered <- struct{}{}
			<-release
			return "fake", provider.Result{Text: "NO_REPLY"}, nil
		})
		g.App = &appchannel.Channel{Agents: g.Agents, Store: db, FilesDir: t.TempDir()}

		askDone := make(chan error, 1)
		go func() {
			_, err := subject(g, ctx, api.RunRequest{Agent: "support", Prompt: "review diff", Session: "code-review", Source: "ask"}, nil)
			askDone <- err
		}()
		handoffRes, err := subject(g, ctx, api.RunRequest{Agent: "assistant", Prompt: "bg task", Session: "bg-1", Async: true, Source: "handoff"}, &api.Target{Channel: "app", Bot: "assistant"})
		require.NoError(t, err)

		followupDone := make(chan error, 1)
		go func() {
			followupDone <- g.RunFollowup(ctx, store.Followup{ID: "f-1", Agent: "support", To: "app:support", Session: "ci-watch", Prompt: "check ci", Quiet: true})
		}()

		for i := 0; i < 3; i++ {
			<-entered
		}

		runs := g.Router.ActiveRuns()
		require.Len(t, runs, 3)
		byKind := map[string]router.ActiveRun{}
		for _, run := range runs {
			byKind[run.Kind] = run
		}
		assert.Equal(t, "code-review", byKind["ask"].Label)
		assert.Equal(t, "support", byKind["ask"].Agent)
		assert.Equal(t, "Tool activity observed", byKind["ask"].Progress)
		assert.False(t, byKind["ask"].AppThread)
		assert.Equal(t, handoffRes.TaskID, byKind["handoff"].TaskID)
		assert.Equal(t, "bg-1", byKind["handoff"].Label)
		assert.False(t, byKind["handoff"].AppThread)
		assert.Equal(t, "ci-watch", byKind["followup"].Label)
		assert.False(t, byKind["followup"].AppThread)

		close(release)
		require.NoError(t, <-askDone)
		require.NoError(t, <-followupDone)
		require.Eventually(t, func() bool { return len(g.Router.ActiveRuns()) == 0 }, time.Second, 5*time.Millisecond)
	})

	t.Run("Classifies scheduled app jobs, plain app turns, non-quiet follow-ups, and rejects unknown sources", func(t *testing.T) {
		ctx := context.Background()
		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		g, db := newGateway(t, &sentMessages{}, func(ctx context.Context, _ provider.Sessions, _ provider.Request) (string, provider.Result, error) {
			entered <- struct{}{}
			<-release
			return "fake", provider.Result{Text: "ok"}, nil
		})
		g.App = &appchannel.Channel{Agents: g.Agents, Store: db, FilesDir: t.TempDir()}

		errs := make(chan error, 4)
		// 1. Synchronous scheduled job delivered to app: must NOT set AppThread.
		go func() {
			_, err := subject(g, ctx, api.RunRequest{Agent: "support", Prompt: "check logs", Session: "nightly"}, &api.Target{Channel: "app", Bot: "support"})
			errs <- err
		}()
		// 2. Plain app turn with no session: joins app:assistant and sets AppThread.
		go func() {
			_, err := subject(g, ctx, api.RunRequest{Agent: "assistant", Prompt: "hello"}, &api.Target{Channel: "app", Bot: "assistant"})
			errs <- err
		}()
		// 3. Unknown/spoofed source without target: ignored and falls back to "run".
		go func() {
			_, err := subject(g, ctx, api.RunRequest{Agent: "support", Prompt: "cli work", Source: "app"}, nil)
			errs <- err
		}()
		// 4. Non-quiet in-thread follow-up (empty session) targeting app: sets AppThread so UI deduplicates it.
		go func() {
			errs <- g.RunFollowup(ctx, store.Followup{ID: "f-2", Agent: "support", To: "app:support", Prompt: "ping", Quiet: false})
		}()

		for i := 0; i < 4; i++ {
			<-entered
		}

		runs := g.Router.ActiveRuns()
		require.Len(t, runs, 4)
		byKind := map[string]router.ActiveRun{}
		for _, run := range runs {
			byKind[run.Kind] = run
		}
		assert.Equal(t, "nightly", byKind["job"].Label)
		assert.False(t, byKind["job"].AppThread, "scheduled job runs in its own session, not the app chat thread")
		assert.True(t, byKind["app"].AppThread)
		assert.Equal(t, "support", byKind["run"].Agent)
		assert.False(t, byKind["run"].AppThread, "spoofed source:app must not hide background runs")
		assert.True(t, byKind["followup"].AppThread, "non-quiet in-thread follow-up joins the app chat thread")

		close(release)
		for i := 0; i < 4; i++ {
			require.NoError(t, <-errs)
		}
		assert.Empty(t, g.Router.ActiveRuns())
	})

	t.Run("Tracks cross-model reviews as read-only pinned runs with isolated sessions", func(t *testing.T) {
		ctx := context.Background()
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var gotReviewReq provider.Request
		g, db := newGateway(t, &sentMessages{}, func(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
			if req.Provider == "claude" {
				gotReviewReq = req
				sessions["claude"] = "claude-review-1"
			} else {
				sessions["codex"] = "codex-job-1"
			}
			entered <- struct{}{}
			<-release
			return req.Provider, provider.Result{Text: "Ready to push."}, nil
		})

		reviewDone := make(chan router.TurnResult, 1)
		go func() {
			res, err := subject(g, ctx, api.RunRequest{
				Agent:    "support",
				Prompt:   "Review the diff on branch feat/demo",
				Session:  "pr-42",
				Provider: "claude",
				Source:   "review",
				Label:    "claude: pr-42",
			}, nil)
			require.NoError(t, err)
			reviewDone <- res
		}()
		askDone := make(chan error, 1)
		go func() {
			_, err := subject(g, ctx, api.RunRequest{
				Agent:   "support",
				Prompt:  "Normal job sharing the same session label",
				Session: "pr-42",
				Source:  "ask",
			}, nil)
			askDone <- err
		}()

		for i := 0; i < 2; i++ {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("review session collided with ask session of the same name")
			}
		}

		runs := g.Router.ActiveRuns()
		require.Len(t, runs, 2)
		byKind := map[string]router.ActiveRun{}
		for _, run := range runs {
			byKind[run.Kind] = run
		}
		assert.Equal(t, "claude: pr-42", byKind["review"].Label)
		assert.Equal(t, "support", byKind["review"].Agent)
		assert.False(t, byKind["review"].AppThread)

		close(release)
		res := <-reviewDone
		require.NoError(t, res.Err)
		require.NoError(t, <-askDone)
		assert.Equal(t, "claude", gotReviewReq.Provider)
		assert.Equal(t, "read-only", gotReviewReq.Settings.CodexSandbox)
		assert.Equal(t, "plan", gotReviewReq.Settings.PermissionMode)
		assert.Equal(t, "plan", gotReviewReq.Settings.AntigravityMode)
		assert.True(t, gotReviewReq.Settings.NoMCP)
		assert.Contains(t, gotReviewReq.Instructions, "read-only code review")

		reviewSessions, err := db.Sessions(ctx, "job:support:review:pr-42")
		require.NoError(t, err)
		assert.Equal(t, "claude-review-1", reviewSessions["claude"])
		jobSessions, err := db.Sessions(ctx, "job:support:pr-42")
		require.NoError(t, err)
		assert.Equal(t, "codex-job-1", jobSessions["codex"])
	})
}
