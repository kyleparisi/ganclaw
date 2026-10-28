package appchannel

import (
	"database/sql"
	"errors"
	"net/http"
)

func (c *Channel) handleRead(w http.ResponseWriter, r *http.Request) {
	thread, ok := c.agentParam(w, r)
	if !ok {
		return
	}
	var req struct {
		MessageID int64 `json:"message_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MessageID <= 0 {
		writeErr(w, 400, "bad_request", "message_id must name a message in this thread")
		return
	}
	if err := c.Store.MarkAppRead(r.Context(), thread, req.MessageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 400, "bad_request", "message_id must name a message in this thread")
		} else {
			writeErr(w, 500, "failed", "could not save read position")
		}
		return
	}
	state, err := c.Store.AppReadState(r.Context(), thread)
	if err != nil {
		writeErr(w, 500, "failed", "could not load read positions")
		return
	}
	c.hub.publish(Event{Type: "read", Thread: thread, Read: &state})
	writeJSON(w, 200, state)
}
