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
