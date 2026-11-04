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
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DoneOpenItemsLimit bounds the completed history returned by OpenItems.
const DoneOpenItemsLimit = 100

var (
	ErrInvalidOpenItem  = errors.New("title is required (max 500 characters); state must be todo, waiting or done; waiting requires waiting_on; follow_up must be YYYY-MM-DD")
	ErrOpenItemConflict = errors.New("open item changed since it was loaded; reload and try again")
)

func normalizeOpenItem(item *OpenItem) error {
	item.Title = strings.TrimSpace(item.Title)
	item.WaitingOn = strings.TrimSpace(item.WaitingOn)
	item.Agent = strings.TrimSpace(item.Agent)
	item.FollowUp = strings.TrimSpace(item.FollowUp)
	if item.State == "" {
		item.State = "todo"
	}
	if item.Title == "" || len(item.Title) > 500 || len(item.WaitingOn) > 500 || len(item.Agent) > 200 || (item.State != "todo" && item.State != "waiting" && item.State != "done") || (item.State == "waiting" && item.WaitingOn == "") {
		return ErrInvalidOpenItem
	}
	if item.FollowUp != "" {
		if _, err := time.Parse("2006-01-02", item.FollowUp); err != nil {
			return ErrInvalidOpenItem
		}
	}
	return nil
}

// SaveOpenItem replaces an existing item, or creates one when ID is empty.
// When updating, a non-zero Version must match the stored version or
// ErrOpenItemConflict is returned; zero skips the check. Each save bumps Version.
func (s *Store) SaveOpenItem(ctx context.Context, item OpenItem) (OpenItem, error) {
	if err := normalizeOpenItem(&item); err != nil {
		return OpenItem{}, err
	}
	item.UpdatedAt = time.Now().UTC()
	if item.ID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return OpenItem{}, err
		}
		item.ID = hex.EncodeToString(b[:])
		item.Version = 1
		data, err := json.Marshal(item)
		if err != nil {
			return OpenItem{}, err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO open_items(id,data) VALUES (?,?)`, item.ID, data)
		return item, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenItem{}, err
	}
	defer tx.Rollback()
	current, err := getOpenItem(ctx, tx, item.ID)
	if err != nil {
		return OpenItem{}, err
	}
	if item.Version != 0 && item.Version != current.Version {
		return OpenItem{}, ErrOpenItemConflict
	}
	item.Version = current.Version + 1
	data, err := json.Marshal(item)
	if err != nil {
		return OpenItem{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE open_items SET data=? WHERE id=?`, data, item.ID); err != nil {
		return OpenItem{}, err
	}
	return item, tx.Commit()
}

// OpenItem returns one item, or sql.ErrNoRows.
func (s *Store) OpenItem(ctx context.Context, id string) (OpenItem, error) {
	return getOpenItem(ctx, s.db, id)
}

func getOpenItem(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (OpenItem, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT data FROM open_items WHERE id=?`, id).Scan(&data); err != nil {
		return OpenItem{}, err
	}
	var item OpenItem
	err := json.Unmarshal(data, &item)
	return item, err
}

// DeleteOpenItem removes an item, or returns sql.ErrNoRows.
func (s *Store) DeleteOpenItem(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM open_items WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// OpenItems lists active items, or the most recent completed ones when done is true.
func (s *Store) OpenItems(ctx context.Context, done bool) ([]OpenItem, error) {
	// julianday parses the RFC 3339 timestamp, so ordering isn't fooled by
	// Go trimming trailing zeros from fractional seconds.
	query := `SELECT data FROM open_items WHERE (json_extract(data,'$.state')='done')=? ORDER BY julianday(json_extract(data,'$.updated_at')) DESC, id`
	args := []any{done}
	if done {
		query += ` LIMIT ?`
		args = append(args, DoneOpenItemsLimit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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
