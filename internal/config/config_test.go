package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ganclaw.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const validConfig = `
state_dir = "/var/lib/ganclaw"

[codex]
home = "/var/lib/ganclaw/codex"

[[agents]]
name = "assistant"
workspace = "/srv/agents/assistant"

[[agents]]
name = "mail"
workspace = "/srv/agents/mail"
instruction_files = ["SOUL.md"]

[[telegram]]
name = "assistant-bot"
agent = "assistant"
token_env = "TEST_TOKEN"
allow_users = [111, 222]
`

func TestLoad(t *testing.T) {
	subject := Load

	t.Run("Valid config gets defaults", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, validConfig))

		require.NoError(t, err)
		assert.Equal(t, "info", cfg.LogLevel)
		assert.Equal(t, []string{"codex", "claude"}, cfg.Providers)
		assert.Equal(t, 20*time.Minute, cfg.TurnTimeout.Duration)
		assert.Equal(t, 7*24*time.Hour, cfg.AttachmentRetention.Duration)
		assert.Equal(t, "/var/lib/ganclaw/codex", cfg.Codex.Home)
		assert.Equal(t, DefaultInstructionFiles, cfg.Agents[0].InstructionFiles)
		assert.Equal(t, []string{"SOUL.md"}, cfg.Agents[1].InstructionFiles)
		assert.Equal(t, []int64{111, 222}, cfg.Telegram[0].AllowUsers)

		a, ok := cfg.Agent("mail")
		assert.True(t, ok)
		assert.Equal(t, "/srv/agents/mail", a.Workspace)
		_, ok = cfg.Agent("nope")
		assert.False(t, ok)
	})

	t.Run("Explicit values override defaults", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, `log_level = "debug"
providers = ["claude"]
turn_timeout = "90s"
`+validConfig))

		require.NoError(t, err)
		assert.Equal(t, "debug", cfg.LogLevel)
		assert.Equal(t, []string{"claude"}, cfg.Providers)
		assert.Equal(t, 90*time.Second, cfg.TurnTimeout.Duration)
	})

	t.Run("Unknown keys are rejected", func(t *testing.T) {
		_, err := subject(writeConfig(t, validConfig+"\n[codex_typo]\nhome = \"x\"\n"))

		assert.ErrorContains(t, err, "unknown keys: codex_typo")
	})

	t.Run("Bot without allow_users is refused", func(t *testing.T) {
		_, err := subject(writeConfig(t, `
state_dir = "/tmp"
[[agents]]
name = "a"
workspace = "/w"
[[telegram]]
name = "open-bot"
agent = "a"
token_env = "T"
`))

		assert.ErrorContains(t, err, `telegram "open-bot": allow_users is required`)
	})

	t.Run("All structural errors are reported together", func(t *testing.T) {
		_, err := subject(writeConfig(t, `
providers = ["codex", "gemini"]
[[agents]]
name = "a"
[[agents]]
name = "a"
workspace = "/w"
[[telegram]]
name = "bot"
agent = "missing"
allow_users = [1]
[[telegram]]
name = "bot"
agent = "a"
token_env = "T"
allow_users = [1]
`))

		require.Error(t, err)
		for _, want := range []string{
			"state_dir is required",
			`unknown provider "gemini"`,
			`agent "a": workspace is required`,
			`duplicate name "a"`,
			`telegram "bot": unknown agent "missing"`,
			`telegram "bot": token_env is required`,
			`telegram: duplicate name "bot"`,
		} {
			assert.ErrorContains(t, err, want)
		}
	})

	t.Run("Transcription needs a model", func(t *testing.T) {
		_, err := subject(writeConfig(t, validConfig+"\n[transcribe]\nwhisper_bin = \"/opt/whisper-cli\"\n"))

		assert.ErrorContains(t, err, "transcribe: model is required")
	})

	t.Run("Transcription section is parsed", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, validConfig+"\n[transcribe]\nwhisper_bin = \"/opt/whisper-cli\"\nmodel = \"/m/small.en.bin\"\nthreads = 2\n"))

		require.NoError(t, err)
		assert.Equal(t, Transcribe{WhisperBin: "/opt/whisper-cli", Model: "/m/small.en.bin", Threads: 2}, cfg.Transcribe)
	})

	t.Run("Contacts and API socket", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, validConfig+`
[[contacts]]
name = "alex"
telegram_chat = 111
default_bot = "assistant-bot"

[openclaw_compat]
fallback_bin = "/opt/openclaw/bin/openclaw"
`))

		require.NoError(t, err)
		assert.Equal(t, "/var/lib/ganclaw/ganclaw.sock", cfg.APISocket, "defaults into state_dir")
		c, ok := cfg.Contact("alex")
		assert.True(t, ok)
		assert.Equal(t, int64(111), c.TelegramChat)
		assert.Equal(t, "/opt/openclaw/bin/openclaw", cfg.OpenClawCompat.FallbackBin)
	})

	t.Run("openclaw_compat defaults and notify validation", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, validConfig))
		require.NoError(t, err)
		assert.Equal(t, "main", cfg.OpenClawCompat.DefaultAgent)

		_, err = subject(writeConfig(t, validConfig+"\n[openclaw_compat]\nnotify = \"nobody\"\n"))
		assert.ErrorContains(t, err, `notify contact "nobody" is not defined`)
	})

	t.Run("MCP servers, web search and agent settings", func(t *testing.T) {
		cfg, err := subject(writeConfig(t, validConfig+`
[[mcp_servers]]
name = "browser"
command = "node"
args = ["cli.js", "--headless"]
env = { PLAYWRIGHT_BROWSERS_PATH = "/opt/pw" }
`))
		require.NoError(t, err)
		assert.Equal(t, "live", cfg.Codex.WebSearch)
		assert.Equal(t, []string{"WebSearch", "WebFetch"}, cfg.Claude.AllowedTools)
		assert.Equal(t, "/opt/pw", cfg.MCPServers[0].Env["PLAYWRIGHT_BROWSERS_PATH"])
	})

	t.Run("MCP servers and settings are validated", func(t *testing.T) {
		body := strings.Replace(validConfig, `name = "mail"`, "name = \"mail\"\nsandbox = \"yolo\"", 1)
		body = strings.Replace(body, `home = "/var/lib/ganclaw/codex"`, "home = \"/var/lib/ganclaw/codex\"\nweb_search = \"sometimes\"", 1)
		_, err := subject(writeConfig(t, body+`
[[mcp_servers]]
name = "bad name"
[[mcp_servers]]
name = "dup"
command = "a"
[[mcp_servers]]
name = "dup"
command = "b"
`))
		require.Error(t, err)
		for _, want := range []string{
			`agent "mail": sandbox must be`,
			`web_search must be live, cached or disabled`,
			`mcp_servers[0]: name must be`,
			`mcp server "bad name": command is required`,
			`mcp_servers: duplicate name "dup"`,
		} {
			assert.ErrorContains(t, err, want)
		}
	})

	t.Run("Contacts are validated", func(t *testing.T) {
		_, err := subject(writeConfig(t, validConfig+`
[[contacts]]
name = "alex"
default_bot = "ghost"
[[contacts]]
name = "alex"
[[contacts]]
telegram_chat = 1
`))

		assert.ErrorContains(t, err, `contact "alex": unknown default_bot "ghost"`)
		assert.ErrorContains(t, err, `contacts: duplicate name "alex"`)
		assert.ErrorContains(t, err, "contacts[2]: name is required")
	})

	t.Run("Invalid duration is an error", func(t *testing.T) {
		_, err := subject(writeConfig(t, `turn_timeout = "soon"`+validConfig))

		assert.ErrorContains(t, err, "soon")
	})

	t.Run("Missing file is an error", func(t *testing.T) {
		_, err := subject(filepath.Join(t.TempDir(), "nope.toml"))

		assert.Error(t, err)
	})
}

func TestTelegramToken(t *testing.T) {
	subject := Telegram{Name: "bot", TokenEnv: "GANCLAW_TEST_TOKEN"}

	t.Run("Reads and trims the named variable", func(t *testing.T) {
		tok, err := subject.Token(func(k string) string {
			assert.Equal(t, "GANCLAW_TEST_TOKEN", k)
			return "  123:abc\n"
		})

		require.NoError(t, err)
		assert.Equal(t, "123:abc", tok)
	})

	t.Run("Empty variable is an error naming it", func(t *testing.T) {
		_, err := subject.Token(func(string) string { return "" })

		assert.ErrorContains(t, err, "GANCLAW_TEST_TOKEN is empty")
	})
}
