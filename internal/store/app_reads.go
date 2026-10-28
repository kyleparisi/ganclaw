package store

import (
	"context"
	"database/sql"
	"errors"
)

// AppReadState is shared by the owner's paired devices. Unread counts finalized
// incoming messages only; hidden task threads are not inbox conversations.
type AppReadState struct {
	LastReadID  int64 `json:"last_read_id"`
	UnreadCount int   `json:"unread_count"`
}

// readStateQuery counts only messages past each thread's read position, so
// the (thread, id) index serves it instead of a scan of all history.
const readStateQuery = `SELECT t.thread, COALESCE(r.message_id, 0),
 (SELECT COUNT(*) FROM app_messages m WHERE m.thread = t.thread AND m.id > COALESCE(r.message_id, 0)
  AND m.role != 'user' AND m.status != 'streaming')
 FROM (SELECT DISTINCT thread FROM app_messages WHERE thread NOT LIKE 'task:%') t
 LEFT JOIN app_reads r ON r.thread = t.thread`

// AppReadState is one thread's read position and unread count.
func (s *Store) AppReadState(ctx context.Context, thread string) (AppReadState, error) {
	states, err := s.readStates(ctx, readStateQuery+` WHERE t.thread = ?`, thread)
	return states[thread], err
}

func (s *Store) AppReadStates(ctx context.Context) (map[string]AppReadState, error) {
	return s.readStates(ctx, readStateQuery)
}

func (s *Store) readStates(ctx context.Context, q string, args ...any) (map[string]AppReadState, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AppReadState{}
	for rows.Next() {
		var thread string
		var state AppReadState
		if err := rows.Scan(&thread, &state.LastReadID, &state.UnreadCount); err != nil {
			return nil, err
		}
		out[thread] = state
	}
	return out, rows.Err()
}

// MarkAppRead advances monotonically to an actual message in this thread. Do
// not cross an unfinished reply: it must still become unread when finalized.
func (s *Store) MarkAppRead(ctx context.Context, thread string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM app_messages WHERE thread = ? AND id = ?`, thread, id).Scan(&found); err != nil {
		return err
	}
	var streaming sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(id) FROM app_messages WHERE thread = ? AND status = 'streaming' AND id <= ?`, thread, id).Scan(&streaming); err != nil {
		return err
	}
	if streaming.Valid {
		id = streaming.Int64 - 1
	}
	if id < 0 {
		return errors.New("invalid read position")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_reads(thread,message_id) VALUES (?,?) ON CONFLICT(thread) DO UPDATE SET message_id = MAX(message_id,excluded.message_id)`, thread, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
