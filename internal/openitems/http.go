// Package openitems shares the open-item API between paired phones and agent tools.
package openitems

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/store"
)

// routes lists the method/path suffixes Handler serves, relative to a prefix
// such as "/v1/open-items".
var routes = []string{"GET ", "POST ", "GET /{item}", "PUT /{item}", "DELETE /{item}"}

// Register mounts Handler on mux for every route under prefix.
func Register(mux interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}, prefix string, db *store.Store) {
	h := Handler(db)
	for _, r := range routes {
		method, rest, _ := strings.Cut(r, " ")
		mux.HandleFunc(method+" "+prefix+rest, h)
	}
}

func Handler(db *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "open_items", "message": message}})
		}
		failErr := func(err error, fallback string) {
			switch {
			case errors.Is(err, store.ErrInvalidOpenItem):
				fail(http.StatusBadRequest, err.Error())
			case errors.Is(err, store.ErrOpenItemConflict):
				fail(http.StatusConflict, err.Error())
			case errors.Is(err, sql.ErrNoRows):
				fail(http.StatusNotFound, "No such open item")
			default:
				fail(http.StatusInternalServerError, fallback)
			}
		}
		if db == nil {
			fail(http.StatusServiceUnavailable, "Open items unavailable")
			return
		}
		id := r.PathValue("item")
		switch {
		case r.Method == http.MethodGet && id == "":
			items, err := db.OpenItems(r.Context(), r.URL.Query().Get("done") == "true")
			if err != nil {
				failErr(err, "Could not load open items")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet:
			item, err := db.OpenItem(r.Context(), id)
			if err != nil {
				failErr(err, "Could not load open item")
				return
			}
			_ = json.NewEncoder(w).Encode(item)
		case r.Method == http.MethodDelete:
			if err := db.DeleteOpenItem(r.Context(), id); err != nil {
				failErr(err, "Could not delete open item")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			var item store.OpenItem
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&item); err != nil {
				fail(http.StatusBadRequest, "Invalid open item")
				return
			}
			item.ID = id // empty for POST, so a body ID can't overwrite another item
			if id == "" {
				item.Version = 0
			}
			saved, err := db.SaveOpenItem(r.Context(), item)
			if err != nil {
				failErr(err, "Could not save open item")
				return
			}
			_ = json.NewEncoder(w).Encode(saved)
		}
	}
}
