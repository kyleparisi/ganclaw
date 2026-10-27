package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTasks(t *testing.T) {
	subject := NewTestStore
	ctx := context.Background()
	t.Run("Persists lifecycle without task content and finds recent tasks", func(t *testing.T) {
		db := subject(t)
		task, err := db.CreateTask(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "queued", task.State)
		assert.Nil(t, task.StartedAt)
		require.NoError(t, db.UpdateTask(ctx, task.ID, "running", "Tool activity observed"))
		running, err := db.Task(ctx, task.ID)
		require.NoError(t, err)
		require.NotNil(t, running.StartedAt)
		assert.Equal(t, "Tool activity observed", running.Progress)
		require.NoError(t, db.UpdateTask(ctx, task.ID, "completed", "Turn completed"))
		require.NoError(t, db.UpdateTask(ctx, task.ID, "running", "late event"))
		done, err := db.Task(ctx, task.ID)
		require.NoError(t, err)
		assert.Equal(t, "completed", done.State)
		assert.NotNil(t, done.FinishedAt)
		tasks, err := db.RecentTasks(ctx, "support")
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		assert.Equal(t, task.ID, tasks[0].ID)
		tasks, err = db.RecentTasks(ctx, "sales")
		require.NoError(t, err)
		assert.Empty(t, tasks)
		data, err := json.Marshal(done)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "prompt")
	})
	t.Run("Restart preserves history and marks unfinished work outcome unknown", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tasks.db")
		db, err := Open(path)
		require.NoError(t, err)
		queued, err := db.CreateTask(ctx, "support")
		require.NoError(t, err)
		running, err := db.CreateTask(ctx, "sales")
		require.NoError(t, err)
		require.NoError(t, db.UpdateTask(ctx, running.ID, "running", "Provider attempt started"))
		require.NoError(t, db.Close())
		db, err = Open(path)
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, db.InterruptTasks(ctx))
		q, err := db.Task(ctx, queued.ID)
		require.NoError(t, err)
		assert.Equal(t, "failed", q.State)
		assert.Nil(t, q.StartedAt)
		assert.Contains(t, q.Progress, "outcome unknown")
		r, err := db.Task(ctx, running.ID)
		require.NoError(t, err)
		assert.Equal(t, "failed", r.State)
		assert.NotNil(t, r.StartedAt)
	})
}
