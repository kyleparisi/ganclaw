package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

func TestSessions(t *testing.T) {
	ctx := context.Background()

	t.Run("Unknown chat has no sessions", func(t *testing.T) {
		subject := NewTestStore(t)

		got, err := subject.Sessions(ctx, "telegram:bot:1")

		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("Save then load, upserting and skipping empty IDs", func(t *testing.T) {
		subject := NewTestStore(t)

		require.NoError(t, subject.SaveSessions(ctx, "telegram:bot:1", provider.Sessions{"codex": "c-1", "claude": ""}))
		require.NoError(t, subject.SaveSessions(ctx, "telegram:bot:1", provider.Sessions{"codex": "c-2", "claude": "k-1"}))
		require.NoError(t, subject.SaveSessions(ctx, "telegram:bot:2", provider.Sessions{"codex": "other"}))

		got, err := subject.Sessions(ctx, "telegram:bot:1")
		require.NoError(t, err)
		assert.Equal(t, provider.Sessions{"codex": "c-2", "claude": "k-1"}, got)
	})

	t.Run("Reset only clears the given chat", func(t *testing.T) {
		subject := NewTestStore(t)
		require.NoError(t, subject.SaveSessions(ctx, "chat-a", provider.Sessions{"codex": "a"}))
		require.NoError(t, subject.SaveSessions(ctx, "chat-b", provider.Sessions{"codex": "b"}))

		require.NoError(t, subject.ResetSessions(ctx, "chat-a"))

		a, err := subject.Sessions(ctx, "chat-a")
		require.NoError(t, err)
		b, err := subject.Sessions(ctx, "chat-b")
		require.NoError(t, err)
		assert.Empty(t, a)
		assert.Equal(t, provider.Sessions{"codex": "b"}, b)
	})

	t.Run("Data survives reopening the database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nested", "dir", "ganclaw.db")
		s1, err := Open(path)
		require.NoError(t, err)
		require.NoError(t, s1.SaveSessions(ctx, "chat", provider.Sessions{"claude": "k-9"}))
		require.NoError(t, s1.SetOffset(ctx, "telegram:bot", 42))
		require.NoError(t, s1.Close())

		subject, err := Open(path)
		require.NoError(t, err)
		defer subject.Close()

		got, err := subject.Sessions(ctx, "chat")
		require.NoError(t, err)
		assert.Equal(t, provider.Sessions{"claude": "k-9"}, got)
		off, err := subject.Offset(ctx, "telegram:bot")
		require.NoError(t, err)
		assert.Equal(t, int64(42), off)
	})
}

func TestOffsets(t *testing.T) {
	ctx := context.Background()

	t.Run("Missing offset is zero", func(t *testing.T) {
		subject := NewTestStore(t)

		got, err := subject.Offset(ctx, "telegram:none")

		require.NoError(t, err)
		assert.Equal(t, int64(0), got)
	})

	t.Run("SetOffset upserts per channel", func(t *testing.T) {
		subject := NewTestStore(t)

		require.NoError(t, subject.SetOffset(ctx, "telegram:a", 10))
		require.NoError(t, subject.SetOffset(ctx, "telegram:a", 11))
		require.NoError(t, subject.SetOffset(ctx, "telegram:b", 99))

		a, err := subject.Offset(ctx, "telegram:a")
		require.NoError(t, err)
		b, err := subject.Offset(ctx, "telegram:b")
		require.NoError(t, err)
		assert.Equal(t, int64(11), a)
		assert.Equal(t, int64(99), b)
	})
}

func TestIdempotency(t *testing.T) {
	ctx := context.Background()

	t.Run("First claim wins; later claims see progress then the response", func(t *testing.T) {
		subject := NewTestStore(t)

		claimed, resp, err := subject.Claim(ctx, "k")
		require.NoError(t, err)
		assert.True(t, claimed)
		assert.Nil(t, resp)

		claimed, resp, err = subject.Claim(ctx, "k")
		require.NoError(t, err)
		assert.False(t, claimed)
		assert.Nil(t, resp, "still in progress")

		require.NoError(t, subject.Complete(ctx, "k", []byte(`{"ok":true}`)))
		claimed, resp, err = subject.Claim(ctx, "k")
		require.NoError(t, err)
		assert.False(t, claimed)
		assert.JSONEq(t, `{"ok":true}`, string(resp))
	})

	t.Run("Release allows a retry", func(t *testing.T) {
		subject := NewTestStore(t)
		_, _, _ = subject.Claim(ctx, "k")

		require.NoError(t, subject.Release(ctx, "k"))
		claimed, _, err := subject.Claim(ctx, "k")

		require.NoError(t, err)
		assert.True(t, claimed)
	})

	t.Run("Prune removes old keys only", func(t *testing.T) {
		subject := NewTestStore(t)
		_, _, _ = subject.Claim(ctx, "old")
		_, _, _ = subject.Claim(ctx, "new")
		_, err := subject.db.ExecContext(ctx, `UPDATE idempotency SET created_at = '2020-01-01T00:00:00.000Z' WHERE key = 'old'`)
		require.NoError(t, err)

		n, err := subject.PruneIdempotency(ctx, 24*time.Hour)

		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		claimed, _, _ := subject.Claim(ctx, "old")
		assert.True(t, claimed)
		claimed, _, _ = subject.Claim(ctx, "new")
		assert.False(t, claimed)
	})
}
