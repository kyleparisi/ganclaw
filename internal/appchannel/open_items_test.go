package appchannel

import (
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOpenItems(t *testing.T) {
	subject := newHarness
	t.Run("Requires pairing and retains items independently of messages", func(t *testing.T) {
		h := subject(t, func(provider.Request) string { return "ok" })
		assert.Equal(t, 401, h.do("GET", "/app/v1/open-items", nil, nil))
		h.pair()
		var item store.OpenItem
		require.Equal(t, 200, h.do("POST", "/app/v1/open-items", store.OpenItem{Title: "Await approval", State: "waiting", WaitingOn: "alex", Agent: "support"}, &item))
		require.NotEmpty(t, item.ID)
		var page struct {
			Items []store.OpenItem `json:"items"`
		}
		require.Equal(t, 200, h.do("GET", "/app/v1/open-items", nil, &page))
		require.Len(t, page.Items, 1)
		item.State = "done"
		require.Equal(t, 200, h.do("PUT", "/app/v1/open-items/"+item.ID, item, &item))
		require.Equal(t, 200, h.do("GET", "/app/v1/open-items", nil, &page))
		assert.Empty(t, page.Items)
		require.Equal(t, 200, h.do("GET", "/app/v1/open-items?done=true", nil, &page))
		require.Len(t, page.Items, 1)
		assert.Equal(t, 400, h.do("POST", "/app/v1/open-items", store.OpenItem{Title: "Missing dependency", State: "waiting"}, nil))
		assert.Equal(t, 404, h.do("PUT", "/app/v1/open-items/missing", store.OpenItem{Title: "Review"}, nil))
	})
}
