package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeName(t *testing.T) {
	subject := SafeName

	t.Run("Strips paths and unsafe characters", func(t *testing.T) {
		assert.Equal(t, "x", subject("../../etc/x"))
		assert.Equal(t, "passwd", subject(`..\\..\\windows\\passwd`))
		assert.Equal(t, "my_report_final.pdf", subject("my report (final).pdf"))
		assert.Equal(t, "résumé.pdf", subject("résumé.pdf"), "letters in other scripts are kept")
		assert.Equal(t, "env", subject(".env"), "no hidden files")
	})

	t.Run("Empty or all-unsafe names get a default", func(t *testing.T) {
		assert.Equal(t, "file", subject(""))
		assert.Equal(t, "file", subject("../.."))
		assert.Equal(t, "file", subject("$%&*"))
	})

	t.Run("Long names are shortened, keeping the extension", func(t *testing.T) {
		got := subject(strings.Repeat("a", 300) + ".pdf")

		assert.Len(t, []rune(got), 100)
		assert.True(t, strings.HasSuffix(got, "aaa.pdf"))
	})
}

func TestSave(t *testing.T) {
	subject := Save

	t.Run("Writes a private file with a unique sanitized name", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".ganclaw", "attachments")

		p1, n, err := subject(dir, "../notes.txt", strings.NewReader("hello"), 1024)
		require.NoError(t, err)
		p2, _, err := subject(dir, "../notes.txt", strings.NewReader("again"), 1024)
		require.NoError(t, err)

		assert.Equal(t, int64(5), n)
		assert.NotEqual(t, p1, p2)
		assert.Equal(t, dir, filepath.Dir(p1))
		assert.True(t, strings.HasSuffix(p1, "-notes.txt"))
		data, _ := os.ReadFile(p1)
		assert.Equal(t, "hello", string(data))
		info, _ := os.Stat(p1)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("Keeps attachments out of git", func(t *testing.T) {
		ws := t.TempDir()

		_, _, err := subject(Dir(ws), "a.txt", strings.NewReader("x"), 10)

		require.NoError(t, err)
		gi, err := os.ReadFile(filepath.Join(ws, ".ganclaw", ".gitignore"))
		require.NoError(t, err)
		assert.Contains(t, string(gi), "*")
	})

	t.Run("Content over the limit is rejected and removed", func(t *testing.T) {
		dir := t.TempDir()

		_, _, err := subject(dir, "big.bin", strings.NewReader(strings.Repeat("x", 11)), 10)

		assert.ErrorIs(t, err, ErrTooLarge)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			assert.Equal(t, ".gitignore", e.Name(), "no partial file left behind")
		}
	})

	t.Run("Content exactly at the limit is kept", func(t *testing.T) {
		_, n, err := subject(t.TempDir(), "ok.bin", strings.NewReader(strings.Repeat("x", 10)), 10)

		require.NoError(t, err)
		assert.Equal(t, int64(10), n)
	})
}

func TestSweep(t *testing.T) {
	subject := Sweep
	now := time.Now()

	t.Run("Removes only files older than the retention", func(t *testing.T) {
		dir := t.TempDir()
		old := filepath.Join(dir, "old.txt")
		fresh := filepath.Join(dir, "fresh.txt")
		require.NoError(t, os.WriteFile(old, []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(fresh, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(old, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)))
		require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o700))

		n, err := subject(dir, 7*24*time.Hour, now)

		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.NoFileExists(t, old)
		assert.FileExists(t, fresh)
		assert.DirExists(t, filepath.Join(dir, "subdir"))
	})

	t.Run("Missing directory is fine", func(t *testing.T) {
		n, err := subject(filepath.Join(t.TempDir(), "nope"), time.Hour, now)

		require.NoError(t, err)
		assert.Equal(t, 0, n)
	})
}
