package gemini

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	subject := New
	t.Run("Runs and resumes a streamed turn", func(t *testing.T) {
		p := subject(Config{Model: "default-model", Environ: func() []string { return []string{"PATH=/bin"} }, Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			assert.Equal(t, "gemini", cmd.Bin)
			assert.Equal(t, []string{"--output-format", "stream-json", "--approval-mode", "default", "--model", "chosen-model", "--resume", "session-1"}, cmd.Args)
			prompt, err := io.ReadAll(cmd.Stdin)
			require.NoError(t, err)
			assert.Contains(t, string(prompt), "Follow these instructions")
			assert.Contains(t, string(prompt), "hello")
			return &procexec.Proc{Stdout: strings.NewReader("{\"type\":\"init\",\"session_id\":\"session-1\"}\n{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"answer\"}\n{\"type\":\"result\",\"status\":\"success\"}"), Wait: func() error { return nil }}, nil
		}}})
		res, err := p.Run(context.Background(), provider.Request{Session: "session-1", Instructions: "Follow these instructions", Prompt: "hello", Settings: provider.Settings{GeminiModel: "chosen-model"}})
		require.NoError(t, err)
		assert.Equal(t, provider.Result{Session: "session-1", Text: "answer"}, res)
	})
	t.Run("Marks a missing executable unavailable", func(t *testing.T) {
		p := subject(Config{Exec: procexec.Exec{Start: func(context.Context, procexec.Cmd) (*procexec.Proc, error) { return nil, errors.New("missing") }}})
		_, err := p.Run(context.Background(), provider.Request{})
		require.ErrorIs(t, err, provider.ErrUnavailable)
	})
	t.Run("Falls back on quota errors", func(t *testing.T) {
		p := subject(Config{Exec: procexec.Exec{Start: func(context.Context, procexec.Cmd) (*procexec.Proc, error) {
			return &procexec.Proc{Stdout: strings.NewReader(`{"type":"error","severity":"error","message":"quota exceeded"}
{"type":"result","status":"error"}`), Wait: func() error { return nil }}, nil
		}}})
		_, err := p.Run(context.Background(), provider.Request{})
		require.ErrorIs(t, err, provider.ErrUnavailable)
	})

	t.Run("Starts a fresh session when the previous one is missing", func(t *testing.T) {
		calls := 0
		p := subject(Config{Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			calls++
			if calls == 1 {
				assert.Contains(t, cmd.Args, "--resume")
				_, _ = io.WriteString(cmd.Stderr, `Error resuming session: Invalid session identifier "old".`)
				return &procexec.Proc{Stdout: strings.NewReader(""), Wait: func() error { return errors.New("exit 42") }}, nil
			}
			assert.NotContains(t, cmd.Args, "--resume")
			return &procexec.Proc{Stdout: strings.NewReader(`{"type":"init","session_id":"new"}
{"type":"message","role":"assistant","content":"done"}
{"type":"result","status":"success"}`), Wait: func() error { return nil }}, nil
		}}})
		res, err := p.Run(context.Background(), provider.Request{Session: "old"})
		require.NoError(t, err)
		assert.Equal(t, "new", res.Session)
		assert.Equal(t, 2, calls)
	})

}
