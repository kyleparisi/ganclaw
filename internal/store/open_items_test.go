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
	t.Run("Rejects stale versions and bumps the version on save", func(t *testing.T) {
		db := NewTestStore(t)
		ctx := context.Background()
		item, err := subject(db, ctx, OpenItem{Title: "Review draft"})
		require.NoError(t, err)
		assert.EqualValues(t, 1, item.Version)
		stale := item
		item.Title = "Review final draft"
		item, err = subject(db, ctx, item)
		require.NoError(t, err)
		assert.EqualValues(t, 2, item.Version)
		stale.Title = "Overwrite"
		_, err = subject(db, ctx, stale)
		require.ErrorIs(t, err, ErrOpenItemConflict)
		stale.Version = 0
		forced, err := subject(db, ctx, stale)
		require.NoError(t, err, "version 0 skips the check")
		assert.EqualValues(t, 3, forced.Version)
		got, err := db.OpenItem(ctx, item.ID)
		require.NoError(t, err)
		assert.Equal(t, forced, got)
	})
	t.Run("Trims fields and deletes items", func(t *testing.T) {
		db := NewTestStore(t)
		ctx := context.Background()
		item, err := subject(db, ctx, OpenItem{Title: " Reply ", Agent: " support ", FollowUp: " 2026-12-01 "})
		require.NoError(t, err)
		assert.Equal(t, OpenItem{ID: item.ID, Title: "Reply", State: "todo", Agent: "support", FollowUp: "2026-12-01", Version: 1, UpdatedAt: item.UpdatedAt}, item)
		require.NoError(t, db.DeleteOpenItem(ctx, item.ID))
		require.ErrorIs(t, db.DeleteOpenItem(ctx, item.ID), sql.ErrNoRows)
		_, err = db.OpenItem(ctx, item.ID)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})
	t.Run("Orders by actual time and bounds completed history", func(t *testing.T) {
		db := NewTestStore(t)
		ctx := context.Background()
		// Go trims trailing zeros: as text ".5Z" sorts after ".55Z" though it is earlier.
		for id, ts := range map[string]string{"earlier": "2026-11-04T00:00:00.5Z", "later": "2026-11-04T00:00:00.55Z", "whole": "2026-11-04T00:00:00Z"} {
			_, err := db.db.ExecContext(ctx, `INSERT INTO open_items(id,data) VALUES (?,json_object('id',?,'title','x','state','todo','updated_at',?))`, id, id, ts)
			require.NoError(t, err)
		}
		items, err := db.OpenItems(ctx, false)
		require.NoError(t, err)
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		assert.Equal(t, []string{"later", "earlier", "whole"}, ids)
		for i := 0; i < DoneOpenItemsLimit+5; i++ {
			_, err := subject(db, ctx, OpenItem{Title: "Done", State: "done"})
			require.NoError(t, err)
		}
		done, err := db.OpenItems(ctx, true)
		require.NoError(t, err)
		assert.Len(t, done, DoneOpenItemsLimit)
	})
}
