package api

import (
	"context"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOpenItems(t *testing.T) {
	subject := serve
	t.Run("Agent socket creates updates and lists shared open items", func(t *testing.T) {
		client := subject(t, &Server{Store: store.NewTestStore(t)})
		ctx := context.Background()
		item, err := client.SaveOpenItem(ctx, store.OpenItem{Title: "Review draft", Agent: "support"})
		require.NoError(t, err)
		items, err := client.OpenItems(ctx, false)
		require.NoError(t, err)
		assert.Equal(t, []store.OpenItem{item}, items)
		item.State = "done"
		_, err = client.SaveOpenItem(ctx, item)
		require.NoError(t, err)
		items, err = client.OpenItems(ctx, false)
		require.NoError(t, err)
		assert.Empty(t, items)
		items, err = client.OpenItems(ctx, true)
		require.NoError(t, err)
		require.Len(t, items, 1)
	})
}
