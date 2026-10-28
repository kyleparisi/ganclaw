package appchannel

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadPositions(t *testing.T) {
	subject := (*harness).do
	t.Run("Authenticated phones share a position and receive a read event", func(t *testing.T) {
		h := newHarness(t, func(provider.Request) string { return "Reply" })
		h.pair()
		name := h.ch.agentNames()[0]
		m, err := h.ch.Store.AddAppMessage(context.Background(), store.AppMessage{Thread: name, Role: store.RoleAgent, Text: "Report"})
		require.NoError(t, err)
		var inbox struct {
			Agents []AgentSummary `json:"agents"`
		}
		require.Equal(t, 200, subject(h, "GET", "/app/v1/agents", nil, &inbox))
		assert.Equal(t, 1, inbox.Agents[0].UnreadCount)
		sub := h.ch.hub.subscribe()
		defer h.ch.hub.unsubscribe(sub)
		var read store.AppReadState
		require.Equal(t, 200, subject(h, "PUT", "/app/v1/threads/"+name+"/read", map[string]int64{"message_id": m.ID}, &read))
		assert.Zero(t, read.UnreadCount)
		assert.Equal(t, m.ID, read.LastReadID)
		select {
		case event := <-sub.ch:
			assert.Equal(t, "read", event.Type)
			require.NotNil(t, event.Read)
			assert.Equal(t, m.ID, event.Read.LastReadID)
		default:
			t.Fatal("missing read event")
		}
		h.pair() // A second device sees the same persisted cursor.
		require.Equal(t, 200, subject(h, "GET", "/app/v1/agents", nil, &inbox))
		assert.Zero(t, inbox.Agents[0].UnreadCount)
		assert.Equal(t, m.ID, inbox.Agents[0].LastReadID)
		require.Equal(t, 400, subject(h, "PUT", "/app/v1/threads/"+name+"/read", map[string]int64{"message_id": m.ID + 1}, nil))
		require.Equal(t, 400, subject(h, "PUT", "/app/v1/threads/"+name+"/read", map[string]int64{"message_id": 0}, nil))
		h.token = ""
		assert.Equal(t, http.StatusUnauthorized, subject(h, "PUT", "/app/v1/threads/"+name+"/read", map[string]int64{"message_id": m.ID}, nil))
	})
}

func TestReadEventsWithMessages(t *testing.T) {
	t.Run("A finished incoming message carries the thread's new unread count", func(t *testing.T) {
		h := newHarness(t, func(provider.Request) string { return "Reply" })
		h.pair()
		sub := h.ch.hub.subscribe()
		defer h.ch.hub.unsubscribe(sub)
		_, err := h.ch.Post(context.Background(), "infra", store.RoleAgent, "Report", "")
		require.NoError(t, err)
		var read *Event
		for read == nil {
			select {
			case e := <-sub.ch:
				if e.Type == "read" {
					read = &e
				}
			case <-time.After(time.Second):
				t.Fatal("no read event")
			}
		}
		assert.Equal(t, "infra", read.Thread)
		assert.Equal(t, 1, read.Read.UnreadCount)
	})
}
