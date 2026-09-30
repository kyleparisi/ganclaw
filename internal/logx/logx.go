// Package logx has small slog helpers.
package logx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
)

// Discard is a logger that drops everything; used when none is configured.
var Discard = slog.New(slog.DiscardHandler)

// OrDiscard returns l, or Discard if l is nil.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return Discard
	}
	return l
}

// LineWriter returns a writer that logs each complete line written to it
// at level, under msg with the line in a "line" attribute. Suitable for a
// subprocess's stderr.
func LineWriter(l *slog.Logger, level slog.Level, msg string) io.Writer {
	return &lineWriter{l: l, level: level, msg: msg}
}

type lineWriter struct {
	l     *slog.Logger
	level slog.Level
	msg   string
	mu    sync.Mutex
	buf   []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(w.buf[:i]); len(line) > 0 {
			w.l.Log(context.Background(), w.level, w.msg, "line", string(line))
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
