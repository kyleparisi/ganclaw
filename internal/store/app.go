package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The app channel keeps its own message history: unlike Telegram, the
// phone has no server-side record of the conversation to fall back on.
const appSchema = `
CREATE TABLE IF NOT EXISTS app_devices (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	last_seen  TEXT
);
CREATE TABLE IF NOT EXISTS app_messages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	thread      TEXT NOT NULL,
	role        TEXT NOT NULL,              -- user | agent | system
	text        TEXT NOT NULL DEFAULT '',
	files       TEXT NOT NULL DEFAULT '[]', -- JSON array of AppFile
	actions     TEXT NOT NULL DEFAULT '[]', -- JSON array of button labels
	answer      TEXT NOT NULL DEFAULT '',   -- the button the user tapped
	status      TEXT NOT NULL DEFAULT 'done', -- streaming | done | error
	job         TEXT NOT NULL DEFAULT '',
	reply_to    INTEGER,
	provider    TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS app_messages_thread ON app_messages (thread, id);
CREATE INDEX IF NOT EXISTS app_messages_job ON app_messages (job, id) WHERE job != '';
CREATE TABLE IF NOT EXISTS app_files (
	id         TEXT PRIMARY KEY,
	thread     TEXT NOT NULL,
	name       TEXT NOT NULL,
	mime       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	path       TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
`

// AppDevice is a paired phone.
type AppDevice struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created_at"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// AppFile is a file sent by either side. Path is never sent to clients.
type AppFile struct {
	ID     string `json:"id"`
	Thread string `json:"thread,omitempty"`
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Path   string `json:"-"`
}

// AppMessage is one message in an app thread.
type AppMessage struct {
	ID       int64     `json:"id"`
	Thread   string    `json:"thread"`
	Role     string    `json:"role"`
	Text     string    `json:"text"`
	Files    []AppFile `json:"files"`
	Actions  []string  `json:"actions"`
	Answer   string    `json:"answer,omitempty"`
	Status   string    `json:"status"`
	Job      string    `json:"job,omitempty"`
	ReplyTo  int64     `json:"reply_to,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
}

// Message roles and statuses.
const (
	RoleUser   = "user"
	RoleAgent  = "agent"
	RoleSystem = "system"

	StatusStreaming = "streaming"
	StatusDone      = "done"
	StatusError     = "error"
)

func (s *Store) initApp() error {
	_, err := s.db.Exec(appSchema)
	return err
}

// HashToken is how device tokens are stored: a leaked database doesn't
// leak usable tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AddAppDevice records a paired device by its token's hash.
func (s *Store) AddAppDevice(ctx context.Context, id, name, token string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_devices (id, name, token_hash) VALUES (?, ?, ?)`, id, name, HashToken(token))
	return err
}

// AppDeviceByToken returns the device a token belongs to and marks it seen.
// ok is false for unknown tokens.
func (s *Store) AppDeviceByToken(ctx context.Context, token string) (d AppDevice, ok bool, err error) {
	var created string
	var seen sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT id, name, created_at, last_seen FROM app_devices WHERE token_hash = ?`, HashToken(token)).
		Scan(&d.ID, &d.Name, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return AppDevice{}, false, nil
	}
	if err != nil {
		return AppDevice{}, false, err
	}
	d.Created = parseTime(created)
	_, err = s.db.ExecContext(ctx, `UPDATE app_devices SET last_seen = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`, d.ID)
	return d, true, err
}

// AppDevices lists paired devices, oldest first.
func (s *Store) AppDevices(ctx context.Context) ([]AppDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_seen FROM app_devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppDevice
	for rows.Next() {
		var d AppDevice
		var created string
		var seen sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen); err != nil {
			return nil, err
		}
		d.Created = parseTime(created)
		if seen.Valid {
			t := parseTime(seen.String)
			d.LastSeen = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemoveAppDevice unpairs a device by ID. It reports whether one existed.
func (s *Store) RemoveAppDevice(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM app_devices WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AddAppFile records a stored file.
func (s *Store) AddAppFile(ctx context.Context, f AppFile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_files (id, thread, name, mime, size, path) VALUES (?, ?, ?, ?, ?, ?)`,
		f.ID, f.Thread, f.Name, f.Mime, f.Size, f.Path)
	return err
}

// AppFile returns a stored file by ID.
func (s *Store) AppFile(ctx context.Context, id string) (AppFile, bool, error) {
	var f AppFile
	err := s.db.QueryRowContext(ctx, `SELECT id, thread, name, mime, size, path FROM app_files WHERE id = ?`, id).
		Scan(&f.ID, &f.Thread, &f.Name, &f.Mime, &f.Size, &f.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return AppFile{}, false, nil
	}
	return f, err == nil, err
}

// AddAppMessage inserts m and returns it with its ID and times set.
func (s *Store) AddAppMessage(ctx context.Context, m AppMessage) (AppMessage, error) {
	if m.Status == "" {
		m.Status = StatusDone
	}
	files, actions, err := encodeLists(m)
	if err != nil {
		return m, err
	}
	var replyTo any
	if m.ReplyTo != 0 {
		replyTo = m.ReplyTo
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO app_messages (thread, role, text, files, actions, status, job, reply_to, provider)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Thread, m.Role, m.Text, files, actions, m.Status, m.Job, replyTo, m.Provider)
	if err != nil {
		return m, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return m, err
	}
	return s.AppMessage(ctx, id)
}

// UpdateAppMessage rewrites a message's text, actions, files, status and
// provider, returning the stored result.
func (s *Store) UpdateAppMessage(ctx context.Context, m AppMessage) (AppMessage, error) {
	files, actions, err := encodeLists(m)
	if err != nil {
		return m, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE app_messages SET text = ?, files = ?, actions = ?, answer = ?, status = ?, provider = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, m.Text, files, actions, m.Answer, m.Status, m.Provider, m.ID); err != nil {
		return m, err
	}
	return s.AppMessage(ctx, m.ID)
}

// AppMessage returns one message by ID.
func (s *Store) AppMessage(ctx context.Context, id int64) (AppMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM app_messages WHERE id = ?`, id)
	if err != nil {
		return AppMessage{}, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return AppMessage{}, err
	}
	if len(msgs) == 0 {
		return AppMessage{}, fmt.Errorf("app message %d not found", id)
	}
	return msgs[0], nil
}

// AppMessages returns up to limit messages of a thread, oldest first,
// ending just before message ID before (0 means the newest).
func (s *Store) AppMessages(ctx context.Context, thread string, before int64, limit int) ([]AppMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + msgCols + ` FROM app_messages WHERE thread = ?`
	args := []any{thread}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// LastAppMessages returns the newest message of every thread, keyed by
// thread.
func (s *Store) LastAppMessages(ctx context.Context) (map[string]AppMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM app_messages
		WHERE id IN (SELECT MAX(id) FROM app_messages GROUP BY thread)`)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]AppMessage, len(msgs))
	for _, m := range msgs {
		out[m.Thread] = m
	}
	return out, nil
}

// AppJobRuns returns the newest agent replies posted by scheduled jobs,
// newest first. An empty job returns runs of every job.
func (s *Store) AppJobRuns(ctx context.Context, job string, limit int) ([]AppMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT ` + msgCols + ` FROM app_messages WHERE job != '' AND role != 'user'`
	args := []any{}
	if job != "" {
		q += ` AND job = ?`
		args = append(args, job)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// FailStreamingAppMessages marks replies left streaming by a crash or
// restart as errors, so clients don't wait on them forever.
func (s *Store) FailStreamingAppMessages(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE app_messages SET status = 'error',
		text = CASE WHEN text = '' THEN 'Interrupted: ganclaw restarted.' ELSE text || char(10) || char(10) || '(Interrupted: ganclaw restarted.)' END,
		updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE status = 'streaming'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const msgCols = `id, thread, role, text, files, actions, answer, status, job, COALESCE(reply_to, 0), provider, created_at, updated_at`

func scanMessages(rows *sql.Rows) ([]AppMessage, error) {
	defer rows.Close()
	var out []AppMessage
	for rows.Next() {
		var m AppMessage
		var files, actions, created, updated string
		if err := rows.Scan(&m.ID, &m.Thread, &m.Role, &m.Text, &files, &actions, &m.Answer, &m.Status, &m.Job, &m.ReplyTo, &m.Provider, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(files), &m.Files); err != nil {
			return nil, fmt.Errorf("app message %d files: %w", m.ID, err)
		}
		if err := json.Unmarshal([]byte(actions), &m.Actions); err != nil {
			return nil, fmt.Errorf("app message %d actions: %w", m.ID, err)
		}
		if m.Files == nil {
			m.Files = []AppFile{}
		}
		if m.Actions == nil {
			m.Actions = []string{}
		}
		m.Created, m.Updated = parseTime(created), parseTime(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

func encodeLists(m AppMessage) (files, actions string, err error) {
	if m.Files == nil {
		m.Files = []AppFile{}
	}
	if m.Actions == nil {
		m.Actions = []string{}
	}
	fb, err := json.Marshal(m.Files)
	if err != nil {
		return "", "", err
	}
	ab, err := json.Marshal(m.Actions)
	if err != nil {
		return "", "", err
	}
	return string(fb), string(ab), nil
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02T15:04:05.999Z", time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
