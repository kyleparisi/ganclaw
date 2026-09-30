package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/router"
)

// streamCursor marks a reply that is still being written.
const streamCursor = " …"

// stream renders a growing reply into one or more Telegram messages,
// editing them at most once per interval to stay within rate limits.
type stream struct {
	client   *Client
	chatID   int64
	interval time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	text     strings.Builder
	dirty    bool
	msgIDs   []int64  // sent messages, in order
	shown    []string // what each message currently shows
	holdTill time.Time
	stop     chan struct{}
	done     chan struct{}
}

// newStream starts a stream whose background flushing ends at Finish or
// when ctx is cancelled.
func newStream(ctx context.Context, client *Client, chatID int64, interval time.Duration, log *slog.Logger) *stream {
	s := &stream{
		client:   client,
		chatID:   chatID,
		interval: interval,
		log:      logx.OrDiscard(log),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.loop(ctx)
	return s
}

func (s *stream) replyStream() *router.ReplyStream {
	return &router.ReplyStream{Append: s.append, Reset: s.reset, Finish: s.finish}
}

func (s *stream) append(t string) {
	s.mu.Lock()
	s.text.WriteString(t)
	s.dirty = true
	s.mu.Unlock()
}

func (s *stream) reset() {
	s.mu.Lock()
	s.text.Reset()
	s.dirty = true
	s.mu.Unlock()
}

func (s *stream) loop(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			s.flush(ctx, false)
		}
	}
}

// finish stops background flushing and shows final as the complete reply.
func (s *stream) finish(ctx context.Context, final string) error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	s.mu.Lock()
	s.text.Reset()
	s.text.WriteString(final)
	s.dirty = true
	s.holdTill = time.Time{}
	s.mu.Unlock()

	err := s.flush(ctx, true)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		// The final text is worth waiting out one rate limit for.
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(apiErr.RetryAfter) * time.Second):
		}
		err = s.flush(ctx, true)
	}
	return err
}

// flush syncs the chat with the current text. Interim flushes show a
// cursor and are skipped while Telegram asks us to back off.
func (s *stream) flush(ctx context.Context, final bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty || (!final && time.Now().Before(s.holdTill)) {
		return nil
	}
	text := s.text.String()
	if !final && strings.TrimSpace(text) == "" && len(s.msgIDs) == 0 {
		return nil // nothing worth showing yet
	}
	if final && strings.TrimSpace(text) == "" {
		text = "(no reply)"
	}

	limit := MaxMessageLen
	if !final {
		limit -= len([]rune(streamCursor))
	}
	parts := SplitText(text, limit)
	if !final {
		parts[len(parts)-1] += streamCursor
	}

	var firstErr error
	for i, part := range parts {
		var err error
		switch {
		case i < len(s.msgIDs) && s.shown[i] == part:
			continue
		case i < len(s.msgIDs):
			err = s.client.EditMessageText(ctx, s.chatID, s.msgIDs[i], part)
		default:
			var id int64
			if id, err = s.client.SendOne(ctx, s.chatID, part); err == nil {
				s.msgIDs = append(s.msgIDs, id)
				s.shown = append(s.shown, "")
			}
		}
		if err != nil {
			s.backoff(err)
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		s.shown[i] = part
	}
	// A reset may leave more messages than the new text needs.
	if firstErr == nil && final {
		for len(s.msgIDs) > len(parts) {
			last := len(s.msgIDs) - 1
			if err := s.client.DeleteMessage(ctx, s.chatID, s.msgIDs[last]); err != nil {
				s.log.Warn("delete surplus message", "err", err)
			}
			s.msgIDs, s.shown = s.msgIDs[:last], s.shown[:last]
		}
	}
	if firstErr == nil {
		s.dirty = false
	}
	return firstErr
}

func (s *stream) backoff(err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		s.holdTill = time.Now().Add(time.Duration(apiErr.RetryAfter) * time.Second)
	}
	s.log.Warn("stream flush failed", "chat_id", s.chatID, "err", err)
}
