package transcribe

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/procexec"
)

// fakeProc returns a finished process with the given output.
func fakeProc(stdout string, waitErr error) *procexec.Proc {
	return &procexec.Proc{
		Stdout: strings.NewReader(stdout),
		Wait:   func() error { return waitErr },
		Kill:   func() error { return nil },
	}
}

func TestTranscribe(t *testing.T) {
	ctx := context.Background()

	t.Run("SuccessFlow", func(t *testing.T) {
		var calls []procexec.Cmd
		var wavDir string
		subject := New(Config{
			Whisper: "/opt/whisper-cli",
			Model:   "/models/ggml-small.en.bin",
			FFmpeg:  "/usr/bin/ffmpeg",
			Threads: 3,
			Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
				calls = append(calls, cmd)
				if cmd.Bin == "/usr/bin/ffmpeg" {
					wav := cmd.Args[len(cmd.Args)-1]
					wavDir = filepath.Dir(wav)
					assert.NoError(t, os.WriteFile(wav, []byte("RIFF"), 0o600))
					return fakeProc("", nil), nil
				}
				return fakeProc("\n And so, my fellow Americans,\n  ask not what your country can do for you.\n", nil), nil
			}},
		})

		got, err := subject.Transcribe(ctx, "/ws/.ganclaw/attachments/voice.ogg")

		require.NoError(t, err)
		assert.Equal(t, "And so, my fellow Americans, ask not what your country can do for you.", got)
		require.Len(t, calls, 2)
		assert.Equal(t, []string{"-nostdin", "-hide_banner", "-loglevel", "error",
			"-i", "/ws/.ganclaw/attachments/voice.ogg", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", "-y", filepath.Join(wavDir, "audio.wav")},
			calls[0].Args)
		assert.Equal(t, "/opt/whisper-cli", calls[1].Bin)
		assert.Equal(t, []string{"-m", "/models/ggml-small.en.bin", "-f", filepath.Join(wavDir, "audio.wav"), "-l", "en", "-t", "3", "-nt", "-np"}, calls[1].Args)
		assert.NoDirExists(t, wavDir, "temporary WAV is cleaned up")
		for _, c := range calls {
			for _, kv := range c.Env {
				assert.True(t, strings.HasPrefix(kv, "PATH="), "only PATH is passed to tools, got %q", kv)
			}
		}
	})

	t.Run("Conversion failure includes ffmpeg's error", func(t *testing.T) {
		subject := New(Config{Whisper: "w", Model: "m", Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			_, _ = io.WriteString(cmd.Stderr, "voice.ogg: Invalid data found when processing input\n")
			return fakeProc("", errors.New("exit status 1")), nil
		}}})

		_, err := subject.Transcribe(ctx, "voice.ogg")

		assert.ErrorContains(t, err, "convert audio: exit status 1: voice.ogg: Invalid data found")
	})

	t.Run("Whisper failure is reported", func(t *testing.T) {
		subject := New(Config{Whisper: "whisper-cli", Model: "missing.bin", Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			if cmd.Bin == "ffmpeg" {
				return fakeProc("", nil), nil
			}
			_, _ = io.WriteString(cmd.Stderr, "error: failed to open 'missing.bin'\n")
			return fakeProc("", errors.New("exit status 2")), nil
		}}})

		_, err := subject.Transcribe(ctx, "voice.ogg")

		assert.ErrorContains(t, err, "whisper: exit status 2: error: failed to open 'missing.bin'")
	})

	t.Run("Missing binary fails cleanly", func(t *testing.T) {
		subject := New(Config{Whisper: "w", Model: "m", Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			return nil, errors.New(`exec: "ffmpeg": executable file not found in $PATH`)
		}}})

		_, err := subject.Transcribe(ctx, "voice.ogg")

		assert.ErrorContains(t, err, "executable file not found")
	})

	t.Run("One transcription runs at a time", func(t *testing.T) {
		var active, maxActive atomic.Int32
		subject := New(Config{Whisper: "w", Model: "m", Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			if cmd.Bin == "ffmpeg" {
				n := active.Add(1)
				if n > maxActive.Load() {
					maxActive.Store(n)
				}
				time.Sleep(20 * time.Millisecond)
				return fakeProc("", nil), nil
			}
			active.Add(-1)
			return fakeProc("text", nil), nil
		}}})

		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := subject.Transcribe(ctx, "voice.ogg")
				assert.NoError(t, err)
			}()
		}
		wg.Wait()

		assert.Equal(t, int32(1), maxActive.Load())
	})

	t.Run("Waiting for a turn respects cancellation", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		var once sync.Once
		subject := New(Config{Whisper: "w", Model: "m", Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			once.Do(func() { close(started) }) // ffmpeg and whisper both start
			<-release
			return fakeProc("", nil), nil
		}}})
		go func() { _, _ = subject.Transcribe(ctx, "first.ogg") }()
		<-started
		cctx, cancel := context.WithCancel(ctx)
		cancel()

		_, err := subject.Transcribe(cctx, "second.ogg")

		assert.ErrorIs(t, err, context.Canceled)
		close(release)
	})
}
