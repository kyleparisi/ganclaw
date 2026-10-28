package appchannel

import (
	"context"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/kyleparisi/ganclaw/internal/store"
)

func (c *Channel) handlePush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	b, err := hex.DecodeString(req.Token)
	if err != nil || len(b) == 0 || len(b) > 512 {
		writeErr(w, 400, "bad_request", "invalid APNs token")
		return
	}
	d := r.Context().Value(ctxKey{}).(store.AppDevice)
	if err := c.Store.SetAppPush(r.Context(), d.ID, hex.EncodeToString(b), newID(16)); err != nil {
		writeErr(w, 500, "failed", "could not save notification registration")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (c *Channel) handleDeletePush(w http.ResponseWriter, r *http.Request) {
	d := r.Context().Value(ctxKey{}).(store.AppDevice)
	if err := c.Store.SetAppPush(r.Context(), d.ID, "", ""); err != nil {
		writeErr(w, 500, "failed", "could not remove notification registration")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (c *Channel) handleUnpair(w http.ResponseWriter, r *http.Request) {
	d := r.Context().Value(ctxKey{}).(store.AppDevice)
	if _, err := c.Store.RemoveAppDevice(r.Context(), d.ID); err != nil {
		writeErr(w, 500, "failed", "could not unpair")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Only deliberate, finalized visible replies call notify; never deltas or hidden task mirrors.
// Work is bounded and best effort: APNs is a hint to reload durable history.
func (c *Channel) notify(m store.AppMessage) {
	if c.Push == nil {
		return
	}
	select {
	case c.pushSlots <- struct{}{}:
	default:
		c.log.Warn("notification queue full")
		return
	}
	go func() {
		defer func() { <-c.pushSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		reads, err := c.Store.AppReadStates(ctx)
		if err != nil {
			c.log.Warn("load notification unread counts failed")
			return
		}
		total := 0
		for name := range c.Agents {
			total += reads[name].UnreadCount
		}
		m.Badge = &total
		devices, err := c.Store.AppPushes(ctx)
		if err != nil {
			c.log.Warn("load notification registrations failed")
			return
		}
		for _, d := range devices {
			invalid, err := c.Push(ctx, d, m)
			if invalid {
				_ = c.Store.RemoveAppPush(ctx, d)
			}
			if err != nil {
				c.log.Warn("notification delivery failed")
			}
		}
	}()
}
