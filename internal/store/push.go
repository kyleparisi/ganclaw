package store

import (
	"context"
	"time"
)

// AppPush is internal routing data, never part of the device API response.
type AppPush struct {
	DeviceID, Token, Generation string
	Registered                  int64
}

// SetAppPush replaces a device's registration. A token can belong to only one pairing.
func (s *Store) SetAppPush(ctx context.Context, device, token, generation string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM app_push WHERE device_id = ? OR token = ?`, device, token); err != nil {
		return err
	}
	if token != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_push(device_id, token, generation, registered) SELECT id, ?, ?, ? FROM app_devices WHERE id = ?`, token, generation, time.Now().UnixMilli(), device); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) AppPushes(ctx context.Context) ([]AppPush, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.device_id, p.token, p.generation, p.registered FROM app_push p JOIN app_devices d ON d.id = p.device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppPush
	for rows.Next() {
		var p AppPush
		if err := rows.Scan(&p.DeviceID, &p.Token, &p.Generation, &p.Registered); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RemoveAppPush cannot remove a registration refreshed while delivery was in flight.
func (s *Store) RemoveAppPush(ctx context.Context, p AppPush) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_push WHERE device_id = ? AND token = ? AND generation = ?`, p.DeviceID, p.Token, p.Generation)
	return err
}
