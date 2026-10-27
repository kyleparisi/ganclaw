package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
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
