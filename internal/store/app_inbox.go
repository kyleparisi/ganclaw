package store

import (
	"context"
	"encoding/json"
)

// AppInboxEntry is a user message waiting to be handed to an agent.
type AppInboxEntry struct {
	ID      int64
	Thread  string
	Prompt  string
	FileIDs []string
}

// AddAppInboxMessage records the visible message and its pending input atomically.
func (s *Store) AddAppInboxMessage(ctx context.Context, m AppMessage, prompt string, fileIDs []string) (AppMessage, error) {
	files, actions, err := encodeLists(m)
	if err != nil {
		return m, err
	}
	ids, err := json.Marshal(fileIDs)
	if err != nil {
		return m, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	var reply any
	if m.ReplyTo != 0 {
		reply = m.ReplyTo
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO app_messages (thread, role, text, files, actions, status, job, reply_to, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.Thread, m.Role, m.Text, files, actions, StatusDone, m.Job, reply, m.Provider)
	if err != nil {
		return m, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return m, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_inbox (message_id, thread, prompt, file_ids) VALUES (?, ?, ?, ?)`, id, m.Thread, prompt, string(ids)); err != nil {
		return m, err
	}
	if err = tx.Commit(); err != nil {
		return m, err
	}
	return s.AppMessage(ctx, id)
}

func (s *Store) AppInbox(ctx context.Context) ([]AppInboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, thread, prompt, file_ids FROM app_inbox ORDER BY message_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AppInboxEntry
	for rows.Next() {
		var e AppInboxEntry
		var ids string
		if err := rows.Scan(&e.ID, &e.Thread, &e.Prompt, &ids); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ids), &e.FileIDs); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) RemoveAppInbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_inbox WHERE message_id = ?`, id)
	return err
}

// RemoveAppInboxBatch atomically hands a group of messages to a turn.
func (s *Store) RemoveAppInboxBatch(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_inbox WHERE message_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
