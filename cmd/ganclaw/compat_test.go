package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
)

func TestParseCompat(t *testing.T) {
	subject := parseCompat

	t.Run("message send, as an alerting script calls it", func(t *testing.T) {
		got, err := subject([]string{"message", "send", "--channel", "telegram", "--account", "alerts",
			"--target", "111", "--message", "health: 2 checks failing", "--json"})

		require.NoError(t, err)
		assert.Equal(t, compatCmd{Kind: "send", Channel: "telegram", Account: "alerts", Target: "111", Message: "health: 2 checks failing", JSON: true}, got)
		assert.Equal(t, api.SendRequest{To: "telegram:alerts:111", Text: "health: 2 checks failing"}, got.sendRequest())
	})

	t.Run("agent with delivery, as a support-ticket job calls it", func(t *testing.T) {
		prompt := filepath.Join(t.TempDir(), "p.txt")
		require.NoError(t, os.WriteFile(prompt, []byte("New support ticket…"), 0o600))

		got, err := subject([]string{"agent", "--agent", "support", "--session-key", "agent:support:telegram:direct:111",
			"--message-file", prompt, "--deliver", "--reply-channel", "telegram", "--reply-account", "support",
			"--reply-to", "111", "--timeout", "720", "--json"})
		require.NoError(t, err)
		req, err := got.runRequest()

		require.NoError(t, err)
		assert.Equal(t, api.RunRequest{Agent: "support", Prompt: "New support ticket…", To: "telegram:support:111", TimeoutSeconds: 720}, req)
	})

	t.Run("Delivered agent run with its own session key stays separate", func(t *testing.T) {
		got, err := subject([]string{"agent", "--agent", "social", "--session-key", "agent:social:dagu-daily-2026-09-30",
			"--message", "do the follows", "--timeout", "1200", "--deliver", "--reply-channel", "telegram",
			"--reply-account", "social", "--reply-to", "111"})
		require.NoError(t, err)
		req, err := got.runRequest()

		require.NoError(t, err)
		assert.Equal(t, "telegram:social:111", req.To)
		assert.Equal(t, "agent:social:dagu-daily-2026-09-30", req.Session)
	})

	t.Run("agent without delivery keeps the session key", func(t *testing.T) {
		got, err := subject([]string{"agent", "--agent", "sales", "--session-key", "agent:sales:lead-triage",
			"--message=review this", "--thinking", "low", "--timeout=120", "--json"})
		require.NoError(t, err)
		req, err := got.runRequest()

		require.NoError(t, err)
		assert.Equal(t, api.RunRequest{Agent: "sales", Prompt: "review this", Session: "agent:sales:lead-triage", TimeoutSeconds: 120}, req)
	})

	t.Run("Unsupported commands, flags and channels", func(t *testing.T) {
		for _, args := range [][]string{
			{"gateway", "run"},
			{"message", "send", "--channel", "slack", "--message", "x"},
			{"agent", "--agent", "a", "--frobnicate", "x"},
			{"agent", "--agent"},
			{"agent", "--timeout", "soon"},
		} {
			_, err := subject(args)
			assert.ErrorIs(t, err, errUnsupported, strings.Join(args, " "))
		}
	})
}

func TestCompatSystemEvent(t *testing.T) {
	cfg := &config.Config{
		Contacts:       []config.Contact{{Name: "alex", TelegramChat: 111}},
		OpenClawCompat: config.OpenClawCompat{DefaultAgent: "main", Notify: "alex"},
	}

	t.Run("Event without a session key goes to the default agent, quietly and async", func(t *testing.T) {
		c, err := parseCompat([]string{"system", "event", "--mode", "now", "--text", "New event listing: Oct 12"})
		require.NoError(t, err)

		req, err := c.eventRequest(cfg)

		require.NoError(t, err)
		assert.Equal(t, "main", req.Agent)
		assert.Equal(t, "alex", req.To)
		assert.Contains(t, req.Prompt, "New event listing: Oct 12")
		assert.Contains(t, req.Prompt, "reply with exactly NO_REPLY")
		assert.True(t, req.Quiet)
		assert.True(t, req.Heartbeat)
		assert.True(t, req.Async, "openclaw returns without waiting unless --expect-final")
	})

	t.Run("Session key picks the agent and the chat", func(t *testing.T) {
		c, err := parseCompat([]string{"system", "event", "--session-key", "agent:support:telegram:direct:111",
			"--text", "ticket", "--expect-final", "--timeout", "90500", "--json", "--port", "18789", "--token=x"})
		require.NoError(t, err)

		req, err := c.eventRequest(cfg)

		require.NoError(t, err)
		assert.Equal(t, "support", req.Agent)
		assert.Equal(t, "alex", req.To)
		assert.False(t, req.Async)
		assert.Equal(t, 91, req.TimeoutSeconds, "milliseconds rounded up to seconds")
	})

	t.Run("Session key without a chat uses the notify contact", func(t *testing.T) {
		c, _ := parseCompat([]string{"system", "event", "--session-key", "agent:sales:lead-triage", "--text", "x"})

		req, err := c.eventRequest(cfg)

		require.NoError(t, err)
		assert.Equal(t, "sales", req.Agent)
		assert.Equal(t, "alex", req.To)
	})

	t.Run("Errors: no text, unknown chat, nobody to notify", func(t *testing.T) {
		c1, _ := parseCompat([]string{"system", "event", "--mode", "now"})
		_, err := c1.eventRequest(cfg)
		assert.ErrorContains(t, err, "--text is required")

		c2, _ := parseCompat([]string{"system", "event", "--session-key", "agent:support:telegram:direct:999", "--text", "x"})
		_, err = c2.eventRequest(cfg)
		assert.ErrorContains(t, err, "no contact has telegram_chat 999")

		c3, _ := parseCompat([]string{"system", "event", "--text", "x"})
		_, err = c3.eventRequest(&config.Config{OpenClawCompat: config.OpenClawCompat{DefaultAgent: "main"}})
		assert.ErrorContains(t, err, "openclaw_compat.notify")
	})
}

func TestExitCode(t *testing.T) {
	subject := exitCode

	t.Run("API error codes map to distinct exit codes", func(t *testing.T) {
		assert.Equal(t, exitUnavailable, subject(&api.Error{Code: api.CodeUnavailable}))
		assert.Equal(t, exitBadAddress, subject(&api.Error{Code: api.CodeUnknownContact}))
		assert.Equal(t, exitBadAddress, subject(&api.Error{Code: api.CodeBadRequest}))
		assert.Equal(t, exitTimeout, subject(&api.Error{Code: api.CodeTimeout}))
		assert.Equal(t, exitBusy, subject(&api.Error{Code: api.CodeInProgress}))
		assert.Equal(t, exitNoServer, subject(&api.Error{Code: api.CodeNoServer}))
		assert.Equal(t, exitFailed, subject(&api.Error{Code: api.CodeFailed}))
		assert.Equal(t, exitFailed, subject(errors.New("other")))
		assert.Equal(t, exitUsage, subject(&exitError{code: exitUsage, err: errors.New("bad flag")}))
	})
}

func TestSocketPath(t *testing.T) {
	subject := socketPath

	t.Run("Flag, then environment, then config", func(t *testing.T) {
		cfg := filepath.Join(t.TempDir(), "g.toml")
		require.NoError(t, os.WriteFile(cfg, []byte(`state_dir = "/state"`+"\n"), 0o600))

		t.Setenv("GANCLAW_SOCKET", "")
		got, err := subject("", cfg)
		require.NoError(t, err)
		assert.Equal(t, "/state/ganclaw.sock", got)

		t.Setenv("GANCLAW_SOCKET", "/env.sock")
		got, _ = subject("", cfg)
		assert.Equal(t, "/env.sock", got)

		got, _ = subject("/flag.sock", cfg)
		assert.Equal(t, "/flag.sock", got)
	})

	t.Run("Missing config explains how to set the socket", func(t *testing.T) {
		t.Setenv("GANCLAW_SOCKET", "")

		_, err := subject("", "/nonexistent.toml")

		assert.ErrorContains(t, err, "GANCLAW_SOCKET")
	})
}

func TestTextArg(t *testing.T) {
	subject := textArg

	t.Run("Arguments or stdin", func(t *testing.T) {
		got, _ := subject([]string{"hello", "world"}, strings.NewReader("ignored"))
		assert.Equal(t, "hello world", got)
		got, _ = subject([]string{"-"}, strings.NewReader("from stdin\n"))
		assert.Equal(t, "from stdin", got)
		got, _ = subject(nil, strings.NewReader("piped"))
		assert.Equal(t, "piped", got)
	})
}

func TestCompatFallback(t *testing.T) {
	subject := compatFallback

	t.Run("Uses the environment and refuses to loop", func(t *testing.T) {
		t.Setenv("GANCLAW_COMPAT_FALLBACK", "")
		t.Setenv("GANCLAW_OPENCLAW_BIN", "/opt/openclaw/bin/openclaw")
		assert.Equal(t, "/opt/openclaw/bin/openclaw", subject())

		t.Setenv("GANCLAW_COMPAT_FALLBACK", "1")
		assert.Empty(t, subject(), "no second hop from inside a fallback")

		t.Setenv("GANCLAW_COMPAT_FALLBACK", "")
		self, _ := os.Executable()
		t.Setenv("GANCLAW_OPENCLAW_BIN", self)
		assert.Empty(t, subject(), "fallback pointing at ganclaw itself is ignored")
	})
}
