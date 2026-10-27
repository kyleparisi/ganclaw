package store

import (
	"context"
	"encoding/json"
)

// AppTask adds a stable history cursor without changing the status-tool API.
type AppTask struct {
	Task
	Cursor int64 `json:"cursor"`
}

// AppTasks filters before pagination so terminal history cannot hide active work.
// Terminal records are archived by lifecycle state, never deleted.
func (s *Store) AppTasks(ctx context.Context, archived bool, before int64) ([]AppTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, data FROM worker_tasks
 WHERE (json_extract(data, '$.state') IN ('completed', 'failed')) = ?
 AND (? = 0 OR rowid < ?) ORDER BY rowid DESC LIMIT 100`, archived, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppTask{}
	for rows.Next() {
		var t AppTask
		var data []byte
		if err := rows.Scan(&t.Cursor, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &t.Task); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
