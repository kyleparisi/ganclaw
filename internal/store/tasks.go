package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Task contains only lifecycle metadata, never prompts, replies or tool arguments.
type Task struct {
	ID         string     `json:"id"`
	Agent      string     `json:"agent"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Progress   string     `json:"progress"`
}

func (s *Store) CreateTask(ctx context.Context, agent string) (Task, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	t := Task{ID: hex.EncodeToString(b[:]), Agent: agent, State: "queued", CreatedAt: now, UpdatedAt: now, Progress: "Waiting in router queue"}
	data, err := json.Marshal(t)
	if err != nil {
		return Task{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO worker_tasks (id, data) VALUES (?, ?)`, t.ID, data)
	return t, err
}

func (s *Store) Task(ctx context.Context, id string) (Task, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM worker_tasks WHERE id = ?`, id).Scan(&data)
	var t Task
	if err == nil {
		err = json.Unmarshal(data, &t)
	}
	return t, err
}

// UpdateTask only accepts active records; terminal states cannot be revived.
func (s *Store) UpdateTask(ctx context.Context, id, state, progress string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stored []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM worker_tasks WHERE id = ?`, id).Scan(&stored); err != nil {
		return err
	}
	var t Task
	if err := json.Unmarshal(stored, &t); err != nil {
		return err
	}
	if t.State == "completed" || t.State == "failed" {
		return nil
	}
	now := time.Now().UTC()
	t.State, t.Progress, t.UpdatedAt = state, progress, now
	if state == "running" && t.StartedAt == nil {
		t.StartedAt = &now
	}
	if state == "completed" || state == "failed" {
		t.FinishedAt = &now
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE worker_tasks SET data = ? WHERE id = ?`, data, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecentTasks returns at most 50 newest tasks, optionally for one agent.
func (s *Store) RecentTasks(ctx context.Context, agent string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM worker_tasks WHERE ? = '' OR json_extract(data, '$.agent') = ? ORDER BY rowid DESC LIMIT 50`, agent, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InterruptTasks runs once at gateway startup. Queues are not durable and
// interrupted work is not automatically retried (it may have side effects).
func (s *Store) InterruptTasks(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE worker_tasks SET data = json_set(data, '$.state', 'failed', '$.progress', 'Gateway restarted; outcome unknown, work was not resumed', '$.updated_at', ?, '$.finished_at', ?) WHERE json_extract(data, '$.state') IN ('queued', 'running')`, now, now)
	return err
}
