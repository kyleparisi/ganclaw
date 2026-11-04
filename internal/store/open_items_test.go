package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenItems(t *testing.T) {
	subject := (*Store).SaveOpenItem
	t.Run("Persists dependencies through edits completion and reopening", func(t *testing.T) {
		db := NewTestStore(t)
		ctx := context.Background()
		item, err := subject(db, ctx, OpenItem{Title: "Review draft", Agent: "support"})
		require.NoError(t, err)
		require.NotEmpty(t, item.ID)
		item.State = "waiting"
		item.WaitingOn = "alex"
		item.FollowUp = "2026-12-01"
		item, err = subject(db, ctx, item)
		require.NoError(t, err)
		active, err := db.OpenItems(ctx, false)
		require.NoError(t, err)
		require.Equal(t, []OpenItem{item}, active)
		item.State = "done"
		item, err = subject(db, ctx, item)
		require.NoError(t, err)
		active, err = db.OpenItems(ctx, false)
		require.NoError(t, err)
		assert.Empty(t, active)
		history, err := db.OpenItems(ctx, true)
		require.NoError(t, err)
		assert.Equal(t, []OpenItem{item}, history)
		item.State = "todo"
		_, err = subject(db, ctx, item)
		require.NoError(t, err)
		active, err = db.OpenItems(ctx, false)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, item.ID, active[0].ID)
	})
	t.Run("Rejects incomplete dependencies invalid dates and missing IDs", func(t *testing.T) {
		db := NewTestStore(t)
		ctx := context.Background()
		_, err := subject(db, ctx, OpenItem{Title: " "})
		require.ErrorIs(t, err, ErrInvalidOpenItem)
		_, err = subject(db, ctx, OpenItem{Title: "Reply", State: "waiting"})
		require.ErrorIs(t, err, ErrInvalidOpenItem)
		_, err = subject(db, ctx, OpenItem{Title: "Reply", FollowUp: "tomorrow"})
		require.ErrorIs(t, err, ErrInvalidOpenItem)
		_, err = subject(db, ctx, OpenItem{Title: "Reply", State: "running"})
		require.ErrorIs(t, err, ErrInvalidOpenItem)
		_, err = subject(db, ctx, OpenItem{ID: "missing", Title: "Reply"})
		require.ErrorIs(t, err, sql.ErrNoRows)
	})
}
