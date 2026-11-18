package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppInbox(t *testing.T) {
	subject := (*Store).AddAppInboxMessage
	t.Run("Messages remain pending until handed to a turn", func(t *testing.T) {
		ctx := context.Background()
		db := NewTestStore(t)
		first, err := subject(db, ctx, AppMessage{Thread: "support", Role: RoleUser, Text: "first"}, "first prompt", []string{"file-1"})
		require.NoError(t, err)
		second, err := subject(db, ctx, AppMessage{Thread: "support", Role: RoleUser, Text: "second"}, "second prompt", nil)
		require.NoError(t, err)
		pending, err := db.AppInbox(ctx)
		require.NoError(t, err)
		require.Len(t, pending, 2)
		assert.Equal(t, first.ID, pending[0].ID)
		assert.Equal(t, []string{"file-1"}, pending[0].FileIDs)
		assert.Equal(t, second.ID, pending[1].ID)
		require.NoError(t, db.RemoveAppInboxBatch(ctx, []int64{first.ID, second.ID}))
		pending, err = db.AppInbox(ctx)
		require.NoError(t, err)
		assert.Empty(t, pending)
		saved, err := db.AppMessage(ctx, first.ID)
		require.NoError(t, err)
		assert.Equal(t, "first", saved.Text)
	})
}
