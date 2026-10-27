package appchannel

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskThreads(t *testing.T) {
	subject := (*Channel).TrackTask
	t.Run("Retains results and files without publishing worker history into the inbox", func(t *testing.T) {
		h := newHarness(t, func(provider.Request) string { return "result" })
		ctx := context.Background()
		task, err := h.ch.Store.CreateTask(ctx, "support")
		require.NoError(t, err)
		sub := h.ch.hub.subscribe()
		defer h.ch.hub.unsubscribe(sub)
		m := h.ch.Message("infra", "work", "")
		// Isolate the history mirror from original delivery to check emitted events.
		delivered := false
		m.Reply = func(context.Context, string) error { delivered = true; return nil }
		m.SendFile = nil
		subject(h.ch, &m, task.ID)
		require.NoError(t, m.Reply(ctx, "Finished work"))
		assert.True(t, delivered)
		path := filepath.Join(t.TempDir(), "result.txt")
		require.NoError(t, os.WriteFile(path, []byte("result"), 0600))
		require.NoError(t, m.SendFile(ctx, path))
		require.NoError(t, m.Reply(ctx, "Additional result"))
		select {
		case e := <-sub.ch:
			t.Fatalf("hidden history published event: %s", e.Type)
		default:
		}
		last, err := h.ch.Store.LastAppMessages(ctx)
		require.NoError(t, err)
		assert.Empty(t, last)
		assert.Equal(t, http.StatusUnauthorized, h.do("GET", "/app/v1/tasks", nil, nil))
		h.pair()
		assert.Empty(t, h.messages("infra"))
		var page struct {
			Tasks []store.AppTask `json:"tasks"`
		}
		assert.Equal(t, 200, h.do("GET", "/app/v1/tasks", nil, &page))
		require.Len(t, page.Tasks, 1)
		assert.Equal(t, "queued", page.Tasks[0].State)
		require.NoError(t, h.ch.Store.UpdateTask(ctx, task.ID, "completed", "Reply delivered"))
		assert.Equal(t, 200, h.do("GET", "/app/v1/tasks", nil, &page))
		assert.Empty(t, page.Tasks)
		assert.Equal(t, 200, h.do("GET", "/app/v1/tasks?archived=true", nil, &page))
		require.Len(t, page.Tasks, 1)
		var history struct {
			Messages []store.AppMessage `json:"messages"`
		}
		assert.Equal(t, 200, h.do("GET", "/app/v1/tasks/"+task.ID+"/messages", nil, &history))
		require.Len(t, history.Messages, 2)
		assert.Equal(t, "Finished work", history.Messages[0].Text)
		require.Len(t, history.Messages[0].Files, 1)
		assert.Equal(t, 200, h.do("GET", "/app/v1/files/"+history.Messages[0].Files[0].ID, nil, nil))
		assert.Equal(t, 404, h.do("GET", "/app/v1/threads/task:"+task.ID+"/messages", nil, nil))
		assert.Equal(t, 404, h.do("GET", "/app/v1/tasks/missing/messages", nil, nil))
		var jobs struct {
			Jobs []JobSummary `json:"jobs"`
		}
		assert.Equal(t, 200, h.do("GET", "/app/v1/jobs", nil, &jobs))
		assert.Empty(t, jobs.Jobs)
	})
}
