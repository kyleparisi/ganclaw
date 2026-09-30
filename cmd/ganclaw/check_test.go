package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/telegram"
)

func TestReadEnvFile(t *testing.T) {
	subject := readEnvFile

	t.Run("KEY=VALUE lines, quotes, comments and export", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "env")
		require.NoError(t, os.WriteFile(p, []byte("# tokens\nA=1:abc\nexport B=\"two\"\n\nC='three'\nbroken line\n"), 0o600))

		got, err := subject(p)

		require.NoError(t, err)
		assert.Equal(t, map[string]string{"A": "1:abc", "B": "two", "C": "three"}, got)
	})
}

func TestCmdCheck(t *testing.T) {
	writeCheckConfig := func(t *testing.T) (cfg, env, state string) {
		dir := t.TempDir()
		cfg = filepath.Join(dir, "g.toml")
		require.NoError(t, os.WriteFile(cfg, []byte(`state_dir = "`+dir+`"
api_socket = "`+dir+`/missing.sock"
[[agents]]
name = "support"
workspace = "/w"
[[telegram]]
name = "support"
agent = "support"
token_env = "GANCLAW_TELEGRAM_SUPPORT_TOKEN"
allow_users = [111]
[[contacts]]
name = "alex"
telegram_chat = 111
default_bot = "support"
[monitor]
notify = "alex"
`), 0o600))
		env = filepath.Join(dir, "env")
		require.NoError(t, os.WriteFile(env, []byte("GANCLAW_TELEGRAM_SUPPORT_TOKEN=123:tok\n"), 0o600))
		return cfg, env, filepath.Join(dir, "state.json")
	}

	t.Run("Gateway down alerts Telegram directly, once", func(t *testing.T) {
		cfg, env, state := writeCheckConfig(t)
		var sent []string
		telegramHTTP = telegram.HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			assert.Contains(t, req.URL.Path, "/bot123:tok/sendMessage")
			var body struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			assert.Equal(t, int64(111), body.ChatID)
			sent = append(sent, body.Text)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
		}}
		t.Cleanup(func() { telegramHTTP = telegram.HTTPClient{} })
		args := []string{"-config", cfg, "-env", env, "-state", state}

		require.NoError(t, cmdCheck(context.Background(), args))
		require.NoError(t, cmdCheck(context.Background(), args))

		require.Len(t, sent, 1, "the second run is quiet")
		assert.Contains(t, sent[0], "⚠️ ganclaw isn't responding")
		assert.FileExists(t, state)
	})

	t.Run("Failed alerts are retried next run", func(t *testing.T) {
		cfg, env, state := writeCheckConfig(t)
		fail := true
		var sent int
		telegramHTTP = telegram.HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			if fail {
				return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":502,"description":"Bad Gateway"}`))}, nil
			}
			sent++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
		}}
		t.Cleanup(func() { telegramHTTP = telegram.HTTPClient{} })
		args := []string{"-config", cfg, "-env", env, "-state", state}

		assert.ErrorContains(t, cmdCheck(context.Background(), args), "will retry")
		fail = false
		require.NoError(t, cmdCheck(context.Background(), args))

		assert.Equal(t, 1, sent)
	})

	t.Run("Dry run prints and saves nothing", func(t *testing.T) {
		cfg, env, state := writeCheckConfig(t)
		telegramHTTP = telegram.HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			t.Error("dry run must not send")
			return nil, nil
		}}
		t.Cleanup(func() { telegramHTTP = telegram.HTTPClient{} })

		require.NoError(t, cmdCheck(context.Background(), []string{"-config", cfg, "-env", env, "-state", state, "-dry-run"}))

		assert.NoFileExists(t, state)
	})

	t.Run("monitor.notify is required", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "g.toml")
		require.NoError(t, os.WriteFile(cfg, []byte(`state_dir = "`+dir+`"`+"\n"), 0o600))

		err := cmdCheck(context.Background(), []string{"-config", cfg})

		assert.ErrorContains(t, err, "monitor.notify")
	})
}
