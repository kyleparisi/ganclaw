package transcribe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration runs real ffmpeg and whisper-cli. It is skipped unless
// GANCLAW_TEST_WHISPER_BIN, GANCLAW_TEST_WHISPER_MODEL and
// GANCLAW_TEST_AUDIO (speech saying "ask not what your country can do")
// are set.
func TestIntegration(t *testing.T) {
	bin, model, audio := os.Getenv("GANCLAW_TEST_WHISPER_BIN"), os.Getenv("GANCLAW_TEST_WHISPER_MODEL"), os.Getenv("GANCLAW_TEST_AUDIO")
	if bin == "" || model == "" || audio == "" {
		t.Skip("set GANCLAW_TEST_WHISPER_BIN, GANCLAW_TEST_WHISPER_MODEL and GANCLAW_TEST_AUDIO to run")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	t.Run("Transcribes an Opus voice note", func(t *testing.T) {
		ogg := filepath.Join(t.TempDir(), "voice.ogg")
		require.NoError(t, exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", audio, "-c:a", "libopus", "-y", ogg).Run())
		subject := New(Config{Whisper: bin, Model: model})

		got, err := subject.Transcribe(context.Background(), ogg)

		require.NoError(t, err)
		assert.Contains(t, strings.ToLower(got), "ask not what your country can do")
	})
}
