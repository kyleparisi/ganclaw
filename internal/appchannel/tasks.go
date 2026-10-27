package appchannel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// TrackTask mirrors final replies and files into a private, read-only task
// thread. No task events or worker typing state enter the ordinary inbox.
func (c *Channel) TrackTask(m *router.Message, id string) {
	c.setup()
	t := &turn{c: c, agent: "task:" + id, hidden: true}
	reply, file := m.Reply, m.SendFile
	m.Reply = func(ctx context.Context, text string) error {
		// Deliver first: a history failure must not suppress the user's result.
		var delivery error
		if reply != nil {
			delivery = reply(ctx, text)
		}
		return errors.Join(delivery, t.reply(ctx, text))
	}
	m.SendFile = func(ctx context.Context, path string) error {
		var delivery error
		if file != nil {
			delivery = file(ctx, path)
		}
		return errors.Join(delivery, t.sendFile(ctx, path))
	}
}

func (c *Channel) handleTasks(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	tasks, err := c.Store.AppTasks(r.Context(), r.URL.Query().Get("archived") == "true", before)
	if err != nil {
		writeErr(w, 500, "failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

func (c *Channel) handleTaskMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("task")
	_, err := c.Store.Task(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "no such task")
		return
	}
	if err != nil {
		writeErr(w, 500, "failed", err.Error())
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	messages, err := c.Store.AppMessages(r.Context(), "task:"+id, before, 100)
	if err != nil {
		writeErr(w, 500, "failed", err.Error())
		return
	}
	if messages == nil {
		messages = []store.AppMessage{}
	}
	writeJSON(w, 200, map[string]any{"messages": messages})
}
