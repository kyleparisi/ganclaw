// Package openitems shares the open-item API between paired phones and agent tools.
package openitems

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kyleparisi/ganclaw/internal/store"
)

func Handler(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "open_items", "message": message}})
		}
		if db == nil {
			fail(503, "Open items unavailable")
			return
		}
		if r.Method == http.MethodGet {
			items, err := db.OpenItems(r.Context(), r.URL.Query().Get("done") == "true")
			if err != nil {
				fail(500, "Could not load open items")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		var item store.OpenItem
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&item); err != nil {
			fail(400, "Invalid open item")
			return
		}
		if r.Method == http.MethodPost {
			item.ID = ""
		} else {
			item.ID = r.PathValue("item")
		}
		saved, err := db.SaveOpenItem(r.Context(), item)
		if errors.Is(err, store.ErrInvalidOpenItem) {
			fail(400, err.Error())
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			fail(404, "No such open item")
			return
		}
		if err != nil {
			fail(500, "Could not save open item")
			return
		}
		_ = json.NewEncoder(w).Encode(saved)
	}
}
