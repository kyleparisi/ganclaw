// Package store persists ganclaw's small amount of state in SQLite:
// per-chat provider sessions and per-bot channel offsets.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	chat_key   TEXT NOT NULL,
	provider   TEXT NOT NULL,
	session_id TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	PRIMARY KEY (chat_key, provider)
);
CREATE TABLE IF NOT EXISTS idempotency (
	key        TEXT PRIMARY KEY,
	response   BLOB,             -- NULL while the request is in progress
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE TABLE IF NOT EXISTS offsets (
	channel    TEXT PRIMARY KEY,
	value      INTEGER NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Sessions returns every provider's session for a chat.
func (s *Store) Sessions(ctx context.Context, chatKey string) (provider.Sessions, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, session_id FROM sessions WHERE chat_key = ?`, chatKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := provider.Sessions{}
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		out[p] = id
	}
	return out, rows.Err()
}

// SaveSessions upserts non-empty sessions for a chat.
func (s *Store) SaveSessions(ctx context.Context, chatKey string, sessions provider.Sessions) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for p, id := range sessions {
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (chat_key, provider, session_id) VALUES (?, ?, ?)
			ON CONFLICT (chat_key, provider) DO UPDATE SET
				session_id = excluded.session_id,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, chatKey, p, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResetSessions forgets a chat's sessions so the next turn starts fresh.
func (s *Store) ResetSessions(ctx context.Context, chatKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE chat_key = ?`, chatKey)
	return err
}

// Offset returns a channel's saved offset, or 0.
func (s *Store) Offset(ctx context.Context, channel string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM offsets WHERE channel = ?`, channel).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// SetOffset saves a channel's offset.
func (s *Store) SetOffset(ctx context.Context, channel string, v int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO offsets (channel, value) VALUES (?, ?)
		ON CONFLICT (channel) DO UPDATE SET
			value = excluded.value,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, channel, v)
	return err
}

// Claim reserves an idempotency key. It returns claimed=true if the caller
// should do the work; otherwise response is the stored result of an earlier
// request (nil if that request is still in progress).
func (s *Store) Claim(ctx context.Context, key string) (claimed bool, response []byte, err error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO idempotency (key) VALUES (?) ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return false, nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT response FROM idempotency WHERE key = ?`, key).Scan(&response)
	return false, response, err
}

// Complete stores the response for a claimed key.
func (s *Store) Complete(ctx context.Context, key string, response []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE idempotency SET response = ? WHERE key = ?`, response, key)
	return err
}

// Release forgets a claimed key so the request can be retried.
func (s *Store) Release(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM idempotency WHERE key = ?`, key)
	return err
}

// PruneIdempotency deletes keys older than the given age.
func (s *Store) PruneIdempotency(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format("2006-01-02T15:04:05.000Z")
	res, err := s.db.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
