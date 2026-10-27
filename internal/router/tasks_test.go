package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressWriter(t *testing.T) {
	subject := progressWriter
	t.Run("Writes at most once per interval and skips repeats", func(t *testing.T) {
		now := time.Unix(0, 0)
		var written []string
		write := subject(func(s string) { written = append(written, s) }, 2*time.Second, func() time.Time { return now })
		write("Tool activity observed")
		write("Agent message activity observed") // too soon
		now = now.Add(3 * time.Second)
		write("Tool activity observed") // same as last written
		write("Agent message activity observed")
		assert.Equal(t, []string{"Tool activity observed", "Agent message activity observed"}, written)
	})
}

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
