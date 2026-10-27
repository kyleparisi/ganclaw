package api

import (
	"context"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskQueries(t *testing.T) {
	subject := serve
	t.Run("Reads one task and discovers recent tasks over the socket", func(t *testing.T) {
		db := store.NewTestStore(t)
		ctx := context.Background()
		task, err := db.CreateTask(ctx, "support")
		require.NoError(t, err)
		client := subject(t, &Server{Store: db})
		one, err := client.Tasks(ctx, task.ID, "")
		require.NoError(t, err)
		require.Len(t, one.Tasks, 1)
		assert.Equal(t, task, one.Tasks[0])
		recent, err := client.Tasks(ctx, "", "support")
		require.NoError(t, err)
		assert.Equal(t, one, recent)
		_, err = client.Tasks(ctx, "missing", "")
		require.Error(t, err)
		var apiErr *Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "unknown_task", apiErr.Code)
	})
	t.Run("Unavailable tracking returns an error", func(t *testing.T) {
		client := subject(t, &Server{})
		_, err := client.Tasks(context.Background(), "", "")
		require.Error(t, err)
	})
}
