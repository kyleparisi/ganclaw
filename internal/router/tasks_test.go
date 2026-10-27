package router

import (
	"context"
	"errors"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskDelivery(t *testing.T) {
	subject := (*Router).process
	t.Run("Successful delivery completes a task", func(t *testing.T) {
		db := store.NewTestStore(t)
		task, err := db.CreateTask(context.Background(), "assistant")
		require.NoError(t, err)
		r := New(context.Background(), Config{Store: db, Agents: testAgents(), Run: func(context.Context, provider.Sessions, provider.Request) (string, provider.Result, error) {
			return "codex", provider.Result{Text: "Done"}, nil
		}})
		subject(r, Message{TaskID: task.ID, ChatKey: "job:support:task", Agent: "assistant", Text: "task", Reply: func(_ context.Context, text string) error { assert.Equal(t, "Done", text); return nil }})
		done, err := db.Task(context.Background(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, "completed", done.State)
		assert.NotNil(t, done.StartedAt)
		assert.NotNil(t, done.FinishedAt)
	})
	t.Run("Delivery errors fail a successful provider turn", func(t *testing.T) {
		db := store.NewTestStore(t)
		task, err := db.CreateTask(context.Background(), "assistant")
		require.NoError(t, err)
		r := New(context.Background(), Config{Store: db, Agents: testAgents(), Run: func(context.Context, provider.Sessions, provider.Request) (string, provider.Result, error) {
			return "codex", provider.Result{Text: "Done"}, nil
		}})
		subject(r, Message{TaskID: task.ID, ChatKey: "job:support:task", Agent: "assistant", Text: "task", Reply: func(context.Context, string) error { return errors.New("private delivery failure") }})
		failed, err := db.Task(context.Background(), task.ID)
		require.NoError(t, err)
		assert.Equal(t, "failed", failed.State)
		assert.NotContains(t, failed.Progress, "private")
	})
}
