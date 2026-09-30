package store

import (
	"path/filepath"
	"testing"
)

// NewTestStore opens a fresh database in a temp dir, closed at test end.
func NewTestStore(t testing.TB) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
