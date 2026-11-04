package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// OpenItem persists a next step independently of conversation and worker lifecycles.
type OpenItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	WaitingOn string    `json:"waiting_on"`
	Agent     string    `json:"agent"`
	FollowUp  string    `json:"follow_up"`
	UpdatedAt time.Time `json:"updated_at"`
}

var ErrInvalidOpenItem = errors.New("title is required (max 500 characters); state must be todo, waiting or done; waiting requires waiting_on; follow_up must be YYYY-MM-DD")

// SaveOpenItem replaces an existing item, or creates one when ID is empty.
func (s *Store) SaveOpenItem(ctx context.Context, item OpenItem) (OpenItem, error) {
	item.Title = strings.TrimSpace(item.Title)
	item.WaitingOn = strings.TrimSpace(item.WaitingOn)
	if item.State == "" {
		item.State = "todo"
	}
	if item.Title == "" || len(item.Title) > 500 || len(item.WaitingOn) > 500 || len(item.Agent) > 200 || (item.State != "todo" && item.State != "waiting" && item.State != "done") || (item.State == "waiting" && item.WaitingOn == "") {
		return OpenItem{}, ErrInvalidOpenItem
	}
	if item.FollowUp != "" {
		if _, err := time.Parse("2006-01-02", item.FollowUp); err != nil {
			return OpenItem{}, ErrInvalidOpenItem
		}
	}
	create := item.ID == ""
	if create {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return OpenItem{}, err
		}
		item.ID = hex.EncodeToString(b[:])
	}
	item.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(item)
	if err != nil {
		return OpenItem{}, err
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO open_items(id,data) VALUES (?,?)`, item.ID, data)
	} else {
		var result sql.Result
		result, err = s.db.ExecContext(ctx, `UPDATE open_items SET data=? WHERE id=?`, data, item.ID)
		if err == nil {
			n, e := result.RowsAffected()
			err = e
			if err == nil && n == 0 {
				err = sql.ErrNoRows
			}
		}
	}
	return item, err
}

func (s *Store) OpenItems(ctx context.Context, done bool) ([]OpenItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM open_items WHERE (json_extract(data,'$.state')='done')=? ORDER BY json_extract(data,'$.updated_at') DESC`, done)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenItem{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var item OpenItem
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
