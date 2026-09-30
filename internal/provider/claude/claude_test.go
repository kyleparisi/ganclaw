package claude

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

// Hand-written stream-json lines matching the shapes emitted by
// `claude -p --output-format stream-json --verbose --include-partial-messages`.
const (
	lineInit        = `{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-test","apiKeySource":"none"}`
	lineThinkStart  = `{"type":"stream_event","session_id":"sess-1","parent_tool_use_id":null,"event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`
	lineThinkDelta  = `{"type":"stream_event","session_id":"sess-1","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`
	lineTextStart   = `{"type":"stream_event","session_id":"sess-1","parent_tool_use_id":null,"event":{"type":"content_block_start","index":1,"content_block":{"type":"text"}}}`
	lineSubagent    = `{"type":"stream_event","session_id":"sess-1","parent_tool_use_id":"toolu_1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"subagent chatter"}}}`
	lineRateAllowed = `{"type":"rate_limit_event","session_id":"sess-1","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","resetsAt":1790740200}}`
	lineRateReject  = `{"type":"rate_limit_event","session_id":"sess-1","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1790740200}}`
	lineSuccess     = `{"type":"result","subtype":"success","is_error":false,"session_id":"sess-1","result":"  Hi there  ","api_error_status":null}`
)

func textDelta(s string) string {
	return `{"type":"stream_event","session_id":"sess-1","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"` + s + `"}}}`
}

func jsonl(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// fakeExec returns an Exec whose process prints stdout, writes stderr, and
// exits with waitErr. The started command is stored in *got.
func fakeExec(got *procexec.Cmd, stdin *string, stdout, stderr string, waitErr error) procexec.Exec {
	return procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
		*got = cmd
		if stdin != nil && cmd.Stdin != nil {
			b, _ := io.ReadAll(cmd.Stdin)
			*stdin = string(b)
		}
		if stderr != "" && cmd.Stderr != nil {
			_, _ = io.WriteString(cmd.Stderr, stderr)
		}
		return &procexec.Proc{
			Stdout: strings.NewReader(stdout),
			Wait:   func() error { return waitErr },
			Kill:   func() error { return nil },
		}, nil
	}}
}

func TestProviderRun(t *testing.T) {
	t.Run("SuccessFlow", func(t *testing.T) {
		var cmd procexec.Cmd
		var stdin string
		subject := New(Config{
			Bin:       "claude-test",
			ConfigDir: "/tmp/claude-config-test",
			Environ: func() []string {
				return []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=sk-should-not-leak", "ANTHROPIC_AUTH_TOKEN=tok", "CLAUDE_CONFIG_DIR=/home/me/.claude", "CLAUDECODE=1"}
			},
			Exec: fakeExec(&cmd, &stdin, jsonl(
				lineInit, lineThinkStart, lineThinkDelta, lineTextStart,
				textDelta("Hi"), lineSubagent, textDelta(" there"), lineRateAllowed, lineSuccess,
			), "", nil),
		})

		var streamed strings.Builder
		res, err := subject.Run(context.Background(), provider.Request{
			Cwd:          "/work/agent",
			Instructions: "Be terse.",
			Prompt:       "--looks-like-a-flag hello",
			OnDelta:      func(s string) { streamed.WriteString(s) },
		})

		require.NoError(t, err)
		assert.Equal(t, "sess-1", res.Session)
		assert.Equal(t, "Hi there", res.Text)
		assert.Equal(t, "Hi there", streamed.String(), "thinking and sub-agent text must not stream")

		assert.Equal(t, "claude-test", cmd.Bin)
		assert.Equal(t, "/work/agent", cmd.Dir)
		assert.Equal(t, "--looks-like-a-flag hello", stdin, "prompt goes on stdin, not argv")
		assert.NotContains(t, cmd.Args, "--looks-like-a-flag hello")
		assert.Subset(t, cmd.Args, []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"})
		assert.Contains(t, strings.Join(cmd.Args, " "), "--permission-mode acceptEdits")
		assert.Contains(t, strings.Join(cmd.Args, " "), "--append-system-prompt Be terse.")
		assert.NotContains(t, cmd.Args, "--resume")

		assert.Equal(t, []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/tmp/claude-config-test"}, cmd.Env,
			"API keys and inherited Claude env must be stripped")
	})

	t.Run("Resume passes --resume and skips instructions", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Model:          "claude-opus-5-5",
			PermissionMode: "bypassPermissions",
			ExtraArgs:      []string{"--strict-mcp-config"},
			Environ:        func() []string { return nil },
			Exec:           fakeExec(&cmd, nil, jsonl(lineInit, lineSuccess), "", nil),
		})

		res, err := subject.Run(context.Background(), provider.Request{Session: "sess-1", Instructions: "ignored", Prompt: "again"})

		require.NoError(t, err)
		assert.Equal(t, "sess-1", res.Session)
		args := strings.Join(cmd.Args, " ")
		assert.Contains(t, args, "--resume sess-1")
		assert.Contains(t, args, "--model claude-opus-5-5")
		assert.Contains(t, args, "--permission-mode bypassPermissions")
		assert.NotContains(t, args, "--append-system-prompt")
		assert.Equal(t, "--strict-mcp-config", cmd.Args[len(cmd.Args)-1])
		assert.Empty(t, cmd.Env, "no CLAUDE_CONFIG_DIR when ConfigDir is unset")
	})

	t.Run("MCP config, allowed tools and per-agent settings", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Model:          "claude-opus-5-5",
			PermissionMode: "acceptEdits",
			MCPConfig:      "/state/claude-mcp.json",
			AllowedTools:   []string{"WebSearch", "mcp__browser"},
			Environ:        func() []string { return nil },
			Exec:           fakeExec(&cmd, nil, jsonl(lineInit, lineSuccess), "", nil),
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x",
			Settings: provider.Settings{ClaudeModel: "claude-sonnet-5-5", PermissionMode: "bypassPermissions"}})

		require.NoError(t, err)
		args := strings.Join(cmd.Args, " ")
		assert.Contains(t, args, "--mcp-config /state/claude-mcp.json --strict-mcp-config")
		assert.Contains(t, args, "--allowedTools WebSearch,mcp__browser")
		assert.Contains(t, args, "--model claude-sonnet-5-5")
		assert.Contains(t, args, "--permission-mode bypassPermissions")
		assert.NotContains(t, args, "claude-opus-5-5")
	})

	t.Run("Separates text blocks around tool calls", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: fakeExec(&cmd, nil, jsonl(
				lineInit, lineTextStart, textDelta("Looking."), lineTextStart, textDelta("Found it."), lineSuccess,
			), "", nil),
		})

		var streamed strings.Builder
		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x", OnDelta: func(s string) { streamed.WriteString(s) }})

		require.NoError(t, err)
		assert.Equal(t, "Looking.\n\nFound it.", streamed.String())
	})

	t.Run("Rejected rate limit triggers fallback", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: fakeExec(&cmd, nil, jsonl(
				lineInit, lineRateReject,
				`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-1","result":"","api_error_status":429}`,
			), "", nil),
		})

		res, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		var cerr *Error
		require.ErrorAs(t, err, &cerr)
		assert.Equal(t, 429, cerr.Status)
		assert.Contains(t, cerr.Message, "five_hour")
		assert.Equal(t, "sess-1", res.Session)
	})

	t.Run("Overloaded API triggers fallback", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: fakeExec(&cmd, nil, jsonl(
				lineInit, `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-1","result":"Overloaded","api_error_status":529}`,
			), "", nil),
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		assert.ErrorContains(t, err, "Overloaded")
	})

	t.Run("Bad request does not trigger fallback", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: fakeExec(&cmd, nil, jsonl(
				lineInit, `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-1","result":"prompt too long","api_error_status":400}`,
			), "", nil),
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		assert.ErrorContains(t, err, "prompt too long")
		assert.False(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("Max turns error does not trigger fallback", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: fakeExec(&cmd, nil, jsonl(
				lineInit, `{"type":"result","subtype":"error_max_turns","is_error":true,"session_id":"sess-1","errors":["hit max turns"]}`,
			), "", nil),
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		var cerr *Error
		require.ErrorAs(t, err, &cerr)
		assert.Equal(t, "error_max_turns", cerr.Subtype)
		assert.Contains(t, cerr.Message, "hit max turns")
		assert.False(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("Exit without result triggers fallback with stderr", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec:    fakeExec(&cmd, nil, "", "Invalid API key · Please run /login\n", errors.New("exit status 1")),
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		assert.ErrorContains(t, err, "Please run /login")
	})

	t.Run("Start failure triggers fallback", func(t *testing.T) {
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
				return nil, errors.New(`exec: "claude": executable file not found in $PATH`)
			}},
		})

		_, err := subject.Run(context.Background(), provider.Request{Prompt: "x"})

		assert.True(t, errors.Is(err, provider.ErrUnavailable))
		assert.ErrorContains(t, err, "executable file not found")
	})

	t.Run("Cancelled context returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec: procexec.Exec{Start: func(c context.Context, pc procexec.Cmd) (*procexec.Proc, error) {
				cmd = pc
				cancel()
				return &procexec.Proc{
					Stdout: strings.NewReader(jsonl(lineInit)),
					Wait:   func() error { return errors.New("signal: interrupt") },
				}, nil
			}},
		})

		res, err := subject.Run(ctx, provider.Request{Prompt: "x"})

		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, "sess-1", res.Session, "session from init is kept for a later resume")
		assert.Equal(t, "claude", cmd.Bin)
	})
}
