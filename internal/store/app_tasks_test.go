package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppTasks(t *testing.T) {
	subject := (*Store).AppTasks
	t.Run("Filters lifecycle before limiting and pages archived history", func(t *testing.T) {
		s := NewTestStore(t)
		ctx := context.Background()
		active, err := s.CreateTask(ctx, "support")
		require.NoError(t, err)
		for i := 0; i < 101; i++ {
			task, err := s.CreateTask(ctx, "sales")
			require.NoError(t, err)
			require.NoError(t, s.UpdateTask(ctx, task.ID, "failed", "Turn failed"))
		}
		tasks, err := subject(s, ctx, false, 0)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		assert.Equal(t, active.ID, tasks[0].ID)
		archive, err := subject(s, ctx, true, 0)
		require.NoError(t, err)
		require.Len(t, archive, 100)
		older, err := subject(s, ctx, true, archive[99].Cursor)
		require.NoError(t, err)
		require.Len(t, older, 1)
		assert.NotEqual(t, archive[99].ID, older[0].ID)
		require.NoError(t, s.InterruptTasks(ctx))
		tasks, err = subject(s, ctx, false, 0)
		require.NoError(t, err)
		assert.Empty(t, tasks)
	})
}
