// Package transcribe turns voice notes into text with ffmpeg and
// whisper.cpp's whisper-cli, running locally.
package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/procexec"
)

type Config struct {
	// Whisper is the whisper-cli executable. Required.
	Whisper string
	// Model is a ggml model file, e.g. ggml-small.en.bin. Required.
	Model string
	// Language passed to whisper-cli; default "en".
	Language string
	// FFmpeg converts audio to 16 kHz mono WAV; default "ffmpeg".
	FFmpeg string
	// Threads for whisper-cli; default 2. Keep it within the CPU the
	// process is allowed: under a systemd CPUQuota of 200%, 4 threads run
	// about 3x slower than 2 because of throttling.
	Threads int
	// Timeout per transcription, including conversion; default 5m.
	Timeout time.Duration
	// Exec starts processes. Zero value uses real processes.
	Exec   procexec.Exec
	Logger *slog.Logger
}

// Transcriber runs one transcription at a time so bursts of voice notes
// can't monopolise the CPU.
type Transcriber struct {
	cfg Config
	log *slog.Logger
	sem chan struct{}
}

func New(cfg Config) *Transcriber {
	if cfg.Language == "" {
		cfg.Language = "en"
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.Threads == 0 {
		cfg.Threads = 2
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.Exec.Start == nil {
		cfg.Exec = procexec.OS
	}
	return &Transcriber{cfg: cfg, log: logx.OrDiscard(cfg.Logger), sem: make(chan struct{}, 1)}
}

// Transcribe returns the text spoken in the audio file at path.
func (t *Transcriber) Transcribe(ctx context.Context, path string) (string, error) {
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	start := time.Now()

	tmp, err := os.MkdirTemp("", "ganclaw-whisper-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	wav := filepath.Join(tmp, "audio.wav")

	if _, err := t.run(ctx, t.cfg.FFmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", path, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-y", wav); err != nil {
		return "", fmt.Errorf("convert audio: %w", err)
	}
	out, err := t.run(ctx, t.cfg.Whisper,
		"-m", t.cfg.Model, "-f", wav, "-l", t.cfg.Language,
		"-t", fmt.Sprint(t.cfg.Threads), "-nt", "-np")
	if err != nil {
		return "", fmt.Errorf("whisper: %w", err)
	}

	text := clean(out)
	t.log.Info("voice note transcribed", "chars", len(text), "duration", time.Since(start))
	return text, nil
}

// clean joins whisper's segment lines into one paragraph.
func clean(out string) string {
	var parts []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

// run executes a command and returns its stdout; errors include the tail
// of stderr.
func (t *Transcriber) run(ctx context.Context, bin string, args ...string) (string, error) {
	var stderr bytes.Buffer
	p, err := t.cfg.Exec.Start(ctx, procexec.Cmd{
		Bin:    bin,
		Args:   args,
		Env:    []string{"PATH=" + os.Getenv("PATH")},
		Stdin:  strings.NewReader(""),
		Stderr: &stderr,
	})
	if err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(p.Stdout)
	if err := p.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = "…" + msg[len(msg)-300:]
		}
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), readErr
}
