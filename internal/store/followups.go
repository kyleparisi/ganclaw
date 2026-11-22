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
	"unicode/utf8"
)

const (
	FollowupPending   = "pending"
	FollowupRunning   = "running"
	FollowupCompleted = "completed"
	FollowupFailed    = "failed"
	FollowupCancelled = "cancelled"

	// MaxPendingFollowupsPerAgent bounds concurrent pending checkbacks per agent.
	MaxPendingFollowupsPerAgent = 20
	// MaxFollowupsPerAgentPerHour caps self-rescheduling checkback chains per agent.
	MaxFollowupsPerAgentPerHour = 20
	// RecentFollowupsLimit caps history returned by ListFollowups when all is true.
	RecentFollowupsLimit = 50
)

var (
	ErrInvalidFollowup    = errors.New("agent, to, prompt (max 4000 characters), and due_at are required")
	ErrTooManyFollowups   = errors.New("too many pending follow-ups for this agent; cancel or wait for existing ones")
	ErrFollowupRateLimit  = errors.New("too many follow-ups scheduled for this agent in the last hour; report status to the user instead of continuing to poll")
	ErrFollowupNotPending = errors.New("follow-up is no longer pending")
)

// Followup is a persisted one-shot delayed checkback that wakes an agent.
type Followup struct {
	ID         string     `json:"id"`
	Agent      string     `json:"agent"`
	To         string     `json:"to"`
	Session    string     `json:"session,omitempty"`
	Prompt     string     `json:"prompt"`
	Quiet      bool       `json:"quiet"`
	DueAt      time.Time  `json:"due_at"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FiredAt    *time.Time `json:"fired_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func normalizeFollowup(f *Followup) error {
	f.Agent = strings.TrimSpace(f.Agent)
	f.To = strings.TrimSpace(f.To)
	f.Session = strings.TrimSpace(f.Session)
	f.Prompt = strings.TrimSpace(f.Prompt)
	if f.Agent == "" || len(f.Agent) > 200 ||
		f.To == "" || len(f.To) > 200 ||
		len(f.Session) > 200 ||
		f.Prompt == "" || len(f.Prompt) > 4000 ||
		f.DueAt.IsZero() {
		return ErrInvalidFollowup
	}
	f.DueAt = f.DueAt.UTC().Truncate(time.Millisecond)
	return nil
}

// CreateFollowup validates and persists a new pending follow-up.
func (s *Store) CreateFollowup(ctx context.Context, f Followup) (Followup, error) {
	if err := normalizeFollowup(&f); err != nil {
		return Followup{}, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Followup{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.ID = hex.EncodeToString(b[:])
	f.State = FollowupPending
	f.Error = ""
	f.CreatedAt = now
	f.UpdatedAt = now
	f.FiredAt = nil
	f.FinishedAt = nil

	data, err := json.Marshal(f)
	if err != nil {
		return Followup{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Followup{}, err
	}
	defer tx.Rollback()

	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM followups WHERE agent = ? AND state = ?`, f.Agent, FollowupPending).Scan(&pending); err != nil {
		return Followup{}, err
	}
	if pending >= MaxPendingFollowupsPerAgent {
		return Followup{}, ErrTooManyFollowups
	}
	hourAgo := now.Add(-time.Hour).UnixMilli()
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM followups WHERE agent = ? AND created_unix_ms >= ?`, f.Agent, hourAgo).Scan(&recent); err != nil {
		return Followup{}, err
	}
	if recent >= MaxFollowupsPerAgentPerHour {
		return Followup{}, ErrFollowupRateLimit
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO followups (id, agent, state, due_unix_ms, created_unix_ms, updated_unix_ms, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Agent, f.State, f.DueAt.UnixMilli(), now.UnixMilli(), now.UnixMilli(), data); err != nil {
		return Followup{}, err
	}
	return f, tx.Commit()
}

// Followup returns a follow-up by ID, or sql.ErrNoRows.
func (s *Store) Followup(ctx context.Context, id string) (Followup, error) {
	return getFollowup(ctx, s.db, id)
}

func getFollowup(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Followup, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT data FROM followups WHERE id = ?`, id).Scan(&data); err != nil {
		return Followup{}, err
	}
	var f Followup
	err := json.Unmarshal(data, &f)
	return f, err
}

// ListFollowups returns pending follow-ups ordered by soonest due, or recent
// follow-ups in all states when all is true. An empty agent matches all agents.
func (s *Store) ListFollowups(ctx context.Context, agent string, all bool) ([]Followup, error) {
	var (
		query string
		args  []any
	)
	if all {
		query = `SELECT data FROM followups
			WHERE (? = '' OR agent = ?)
			ORDER BY CASE state WHEN 'pending' THEN 0 WHEN 'running' THEN 1 ELSE 2 END ASC,
				updated_unix_ms DESC, id ASC
			LIMIT ?`
		args = []any{agent, agent, RecentFollowupsLimit}
	} else {
		query = `SELECT data FROM followups
			WHERE state = ? AND (? = '' OR agent = ?)
			ORDER BY due_unix_ms ASC, id ASC`
		args = []any{FollowupPending, agent, agent}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Followup{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var f Followup
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// NextFollowupDue returns the earliest due_at among pending follow-ups.
func (s *Store) NextFollowupDue(ctx context.Context) (time.Time, bool, error) {
	var ms sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(due_unix_ms) FROM followups WHERE state = ?`, FollowupPending).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if !ms.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(ms.Int64).UTC(), true, nil
}

// ClaimDueFollowups atomically transitions up to limit pending follow-ups due
// at or before now to running and returns them in due order. A non-positive
// limit claims all due rows. Corrupt rows are marked failed so they cannot
// poison or spin the scheduler loop.
func (s *Store) ClaimDueFollowups(ctx context.Context, now time.Time, limit int) ([]Followup, error) {
	now = now.UTC().Truncate(time.Millisecond)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `SELECT id, agent, data FROM followups WHERE state = ? AND due_unix_ms <= ? ORDER BY due_unix_ms ASC, id ASC`
	args := []any{FollowupPending, now.UnixMilli()}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type corruptRow struct {
		id    string
		agent string
	}
	var claimed []Followup
	var corrupt []corruptRow
	for rows.Next() {
		var (
			id    string
			agent string
			raw   []byte
		)
		if err := rows.Scan(&id, &agent, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var f Followup
		if err := json.Unmarshal(raw, &f); err != nil {
			corrupt = append(corrupt, corruptRow{id: id, agent: agent})
			continue
		}
		claimed = append(claimed, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, c := range corrupt {
		finishedAt := now
		failed := Followup{
			ID:         c.id,
			Agent:      c.agent,
			State:      FollowupFailed,
			Error:      "corrupt follow-up record",
			UpdatedAt:  now,
			FinishedAt: &finishedAt,
		}
		data, err := json.Marshal(failed)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE followups SET state = ?, updated_unix_ms = ?, data = ? WHERE id = ?`,
			FollowupFailed, now.UnixMilli(), data, c.id); err != nil {
			return nil, err
		}
	}

	for i := range claimed {
		firedAt := now
		claimed[i].State = FollowupRunning
		claimed[i].FiredAt = &firedAt
		claimed[i].UpdatedAt = now
		data, err := json.Marshal(claimed[i])
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE followups SET state = ?, updated_unix_ms = ?, data = ? WHERE id = ?`,
			FollowupRunning, now.UnixMilli(), data, claimed[i].ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// FinishFollowup records the terminal outcome of a running follow-up.
func (s *Store) FinishFollowup(ctx context.Context, id, state, errMsg string) error {
	if state != FollowupCompleted && state != FollowupFailed {
		return ErrInvalidFollowup
	}
	errMsg = truncateUTF8(strings.TrimSpace(errMsg), 500)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	f, err := getFollowup(ctx, tx, id)
	if err != nil {
		return err
	}
	if f.State != FollowupRunning {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.State = state
	f.Error = errMsg
	f.FinishedAt = &now
	f.UpdatedAt = now
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE followups SET state = ?, updated_unix_ms = ?, data = ? WHERE id = ? AND state = ?`,
		state, now.UnixMilli(), data, id, FollowupRunning); err != nil {
		return err
	}
	return tx.Commit()
}

// CancelFollowup cancels a pending follow-up. Returns sql.ErrNoRows if missing
// or ErrFollowupNotPending if it already fired or finished.
func (s *Store) CancelFollowup(ctx context.Context, id string) (Followup, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Followup{}, err
	}
	defer tx.Rollback()

	f, err := getFollowup(ctx, tx, id)
	if err != nil {
		return Followup{}, err
	}
	if f.State != FollowupPending {
		return Followup{}, ErrFollowupNotPending
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.State = FollowupCancelled
	f.FinishedAt = &now
	f.UpdatedAt = now
	data, err := json.Marshal(f)
	if err != nil {
		return Followup{}, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE followups SET state = ?, updated_unix_ms = ?, data = ? WHERE id = ? AND state = ?`,
		FollowupCancelled, now.UnixMilli(), data, id, FollowupPending)
	if err != nil {
		return Followup{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Followup{}, err
	} else if n == 0 {
		return Followup{}, ErrFollowupNotPending
	}
	return f, tx.Commit()
}

// InterruptFollowups marks follow-ups that were running before a restart as
// failed so they are not silently retried, returning the affected records so
// the caller can notify recipients. Pending follow-ups remain pending.
func (s *Store) InterruptFollowups(ctx context.Context) ([]Followup, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id, agent, data FROM followups WHERE state = ?`, FollowupRunning)
	if err != nil {
		return nil, err
	}
	var running []Followup
	for rows.Next() {
		var (
			id    string
			agent string
			raw   []byte
		)
		if err := rows.Scan(&id, &agent, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var f Followup
		if err := json.Unmarshal(raw, &f); err != nil {
			f = Followup{ID: id, Agent: agent}
		}
		running = append(running, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range running {
		running[i].State = FollowupFailed
		running[i].Error = "Gateway restarted while follow-up was running; not retried"
		running[i].FinishedAt = &now
		running[i].UpdatedAt = now
		data, err := json.Marshal(running[i])
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE followups SET state = ?, updated_unix_ms = ?, data = ? WHERE id = ?`,
			FollowupFailed, now.UnixMilli(), data, running[i].ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return running, nil
}

// PruneFollowups deletes completed, failed, or cancelled follow-ups older than olderThan.
func (s *Store) PruneFollowups(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM followups WHERE state IN (?, ?, ?) AND updated_unix_ms < ?`,
		FollowupCompleted, FollowupFailed, FollowupCancelled, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
