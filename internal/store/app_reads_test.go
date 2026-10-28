package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppReadStates(t *testing.T) {
	subject := (*Store).AppReadStates
	t.Run("Counts incoming finalized messages and preserves monotonic positions", func(t *testing.T) {
		s := NewTestStore(t)
		ctx := context.Background()
		first, err := s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleAgent, Text: "Hello"})
		require.NoError(t, err)
		_, err = s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleUser, Text: "Question"})
		require.NoError(t, err)
		second, err := s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleSystem, Status: StatusError, Text: "Failed", Job: "daily"})
		require.NoError(t, err)
		_, err = s.AddAppMessage(ctx, AppMessage{Thread: "task:example", Role: RoleAgent, Text: "Hidden"})
		require.NoError(t, err)
		states, err := subject(s, ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, states["support"].UnreadCount)
		assert.NotContains(t, states, "task:example")
		require.NoError(t, s.MarkAppRead(ctx, "support", second.ID))
		require.NoError(t, s.MarkAppRead(ctx, "support", first.ID))
		states, err = subject(s, ctx)
		require.NoError(t, err)
		assert.Equal(t, second.ID, states["support"].LastReadID)
		assert.Zero(t, states["support"].UnreadCount)
		assert.Error(t, s.MarkAppRead(ctx, "sales", first.ID))
		assert.Error(t, s.MarkAppRead(ctx, "support", second.ID+100))
	})
	t.Run("A streaming reply remains unread when it finishes", func(t *testing.T) {
		s := NewTestStore(t)
		ctx := context.Background()
		draft, err := s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleAgent, Status: StatusStreaming})
		require.NoError(t, err)
		last, err := s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleUser, Text: "Another question"})
		require.NoError(t, err)
		require.NoError(t, s.MarkAppRead(ctx, "support", last.ID))
		states, err := subject(s, ctx)
		require.NoError(t, err)
		assert.Less(t, states["support"].LastReadID, draft.ID)
		assert.Zero(t, states["support"].UnreadCount)
		draft.Status = StatusDone
		draft.Text = "Reply"
		_, err = s.UpdateAppMessage(ctx, draft)
		require.NoError(t, err)
		states, err = subject(s, ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, states["support"].UnreadCount)
	})
	t.Run("Upgrading marks existing history read once", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "upgrade.db")
		s, err := Open(path)
		require.NoError(t, err)
		ctx := context.Background()
		_, err = s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleAgent, Text: "Old report"})
		require.NoError(t, err)
		_, err = s.db.Exec(`DROP TABLE app_reads`) // a database from before read state
		require.NoError(t, err)
		require.NoError(t, s.Close())

		s, err = Open(path)
		require.NoError(t, err)
		defer s.Close()
		states, err := subject(s, ctx)
		require.NoError(t, err)
		assert.Zero(t, states["support"].UnreadCount, "old history isn't suddenly unread")
		_, err = s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleAgent, Text: "New report"})
		require.NoError(t, err)
		_, err = s.AddAppMessage(ctx, AppMessage{Thread: "sales", Role: RoleAgent, Text: "First"})
		require.NoError(t, err)
		require.NoError(t, s.Close())
		s, err = Open(path) // later restarts don't mark anything read
		require.NoError(t, err)
		defer s.Close()
		states, err = subject(s, ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, states["support"].UnreadCount)
		assert.Equal(t, 1, states["sales"].UnreadCount)
		one, err := s.AppReadState(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, states["support"], one)
	})
	t.Run("Read positions survive reopening the database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read.db")
		s, err := Open(path)
		require.NoError(t, err)
		ctx := context.Background()
		m, err := s.AddAppMessage(ctx, AppMessage{Thread: "support", Role: RoleAgent, Text: "Report"})
		require.NoError(t, err)
		require.NoError(t, s.MarkAppRead(ctx, "support", m.ID))
		require.NoError(t, s.Close())
		s, err = Open(path)
		require.NoError(t, err)
		defer s.Close()
		states, err := subject(s, ctx)
		require.NoError(t, err)
		assert.Equal(t, m.ID, states["support"].LastReadID)
		assert.Zero(t, states["support"].UnreadCount)
	})
}
