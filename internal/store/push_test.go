package store

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAppPush(t *testing.T) {
	subject := (*Store).SetAppPush
	t.Run("Rotation transfer revocation and stale responses preserve association", func(t *testing.T) {
		ctx := context.Background()
		s := NewTestStore(t)
		require.NoError(t, s.AddAppDevice(ctx, "a", "phone", "a-token"))
		require.NoError(t, s.AddAppDevice(ctx, "b", "tablet", "b-token"))
		require.NoError(t, subject(s, ctx, "a", "first", "1"))
		old, err := s.AppPushes(ctx)
		require.NoError(t, err)
		require.Len(t, old, 1)
		require.NoError(t, subject(s, ctx, "a", "second", "2"))
		require.NoError(t, s.RemoveAppPush(ctx, old[0]))
		current, err := s.AppPushes(ctx)
		require.NoError(t, err)
		require.Len(t, current, 1)
		assert.Equal(t, "second", current[0].Token)
		require.NoError(t, subject(s, ctx, "b", "second", "3"))
		current, err = s.AppPushes(ctx)
		require.NoError(t, err)
		require.Len(t, current, 1)
		assert.Equal(t, "b", current[0].DeviceID)
		_, err = s.RemoveAppDevice(ctx, "b")
		require.NoError(t, err)
		current, err = s.AppPushes(ctx)
		require.NoError(t, err)
		assert.Empty(t, current)
		require.NoError(t, subject(s, ctx, "b", "second", "4"))
		current, err = s.AppPushes(ctx)
		require.NoError(t, err)
		assert.Empty(t, current)
	})
}
