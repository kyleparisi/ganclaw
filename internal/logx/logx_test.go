package logx

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineWriter(t *testing.T) {
	subject := LineWriter

	t.Run("Logs each complete line and buffers partial ones", func(t *testing.T) {
		var out bytes.Buffer
		l := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
		w := subject(l, slog.LevelDebug, "child stderr")

		_, _ = io.WriteString(w, "first li")
		assert.Empty(t, out.String(), "partial line must not be logged yet")
		_, _ = io.WriteString(w, "ne\n\n   \nsecond\nthi")

		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		assert.Len(t, lines, 2, "blank lines are skipped")
		assert.Contains(t, lines[0], `msg="child stderr" line="first line"`)
		assert.Contains(t, lines[1], `line=second`)
	})

	t.Run("Respects the logger level", func(t *testing.T) {
		var out bytes.Buffer
		l := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
		w := subject(l, slog.LevelDebug, "child stderr")

		_, _ = io.WriteString(w, "noisy\n")

		assert.Empty(t, out.String())
	})
}

func TestOrDiscard(t *testing.T) {
	subject := OrDiscard

	t.Run("Nil becomes a discard logger", func(t *testing.T) {
		assert.Same(t, Discard, subject(nil))
	})

	t.Run("Non-nil is returned as is", func(t *testing.T) {
		l := slog.Default()
		assert.Same(t, l, subject(l))
	})
}
