// Package attachments stores files users send so agents can read them.
package attachments

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Dir is where a workspace's attachments live.
func Dir(workspace string) string { return filepath.Join(workspace, ".ganclaw", "attachments") }

// ErrTooLarge is returned by Save when the content exceeds its limit.
var ErrTooLarge = errors.New("attachment too large")

// Save writes r into dir under a unique, sanitized version of name and
// returns the absolute path and size. Content beyond max bytes is an
// ErrTooLarge error and nothing is kept.
func Save(dir, name string, r io.Reader, max int64) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	ensureGitignore(dir)

	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	path := filepath.Join(dir, fmt.Sprintf("%s-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(suffix[:]), SafeName(name)))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("%w: over %d bytes", ErrTooLarge, max)
	}
	if err != nil {
		os.Remove(path)
		return "", 0, err
	}
	abs, err := filepath.Abs(path)
	return abs, n, err
}

// SafeName reduces a user-supplied file name to a harmless base name.
func SafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if r := []rune(s); len(r) > 100 {
		// Keep the extension when shortening.
		ext := filepath.Ext(s)
		if len([]rune(ext)) > 10 {
			ext = ""
		}
		s = string(r[:100-len([]rune(ext))]) + ext
	}
	if s == "" {
		return "file"
	}
	return s
}

// ensureGitignore keeps attachments out of any git repo the workspace is in.
func ensureGitignore(dir string) {
	gi := filepath.Join(filepath.Dir(dir), ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(gi, []byte("# ganclaw runtime files\n*\n"), 0o600)
	}
}

// Sweep deletes files in dir last modified before now-olderThan. A missing
// dir is not an error.
func Sweep(dir string, olderThan time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > olderThan {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				errs = append(errs, err)
			} else {
				removed++
			}
		}
	}
	return removed, errors.Join(errs...)
}
