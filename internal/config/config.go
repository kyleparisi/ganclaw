// Package config loads ganclaw's TOML configuration. Secrets are never
// stored in the file: bots reference environment variables by name.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	LogLevel string `toml:"log_level"`
	// StateDir holds ganclaw's SQLite database.
	StateDir string `toml:"state_dir"`
	// Providers is the fallback order, e.g. ["codex", "claude"].
	Providers []string `toml:"providers"`
	// TurnTimeout bounds a single agent turn across all providers.
	TurnTimeout Duration `toml:"turn_timeout"`
	// AttachmentRetention is how long files users send are kept in
	// <workspace>/.ganclaw/attachments.
	AttachmentRetention Duration `toml:"attachment_retention"`
	// APISocket is the local API's Unix socket; default
	// <state_dir>/ganclaw.sock. Only its owner can connect.
	APISocket string `toml:"api_socket"`

	Codex      Codex       `toml:"codex"`
	Claude     Claude      `toml:"claude"`
	Transcribe Transcribe  `toml:"transcribe"`
	Agents     []Agent     `toml:"agents"`
	Telegram   []Telegram  `toml:"telegram"`
	Contacts   []Contact   `toml:"contacts"`
	MCPServers []MCPServer `toml:"mcp_servers"`

	OpenClawCompat OpenClawCompat `toml:"openclaw_compat"`

	Monitor Monitor `toml:"monitor"`

	// DisableAgentTools stops ganclaw registering its own MCP server
	// ("ganclaw": send_message, ask_agent, hand_off, list_agents) with
	// every agent.
	DisableAgentTools bool `toml:"disable_agent_tools"`
}

// Contact names a person so scripts can address them without chat IDs.
type Contact struct {
	Name string `toml:"name"`
	// TelegramChat is the person's chat ID (their user ID for private
	// chats), the same for every bot.
	TelegramChat int64 `toml:"telegram_chat"`
	// DefaultBot is used when a request doesn't pick one with --via.
	DefaultBot string `toml:"default_bot"`
}

// Monitor configures `ganclaw check`.
type Monitor struct {
	// Notify is the contact that receives alerts. Required for check.
	Notify string `toml:"notify"`
	// Via is the bot alerts are sent through; default the contact's
	// default_bot. Its token is also used to alert directly when ganclaw
	// itself is down.
	Via string `toml:"via"`
	// BotStaleAfter: a bot that hasn't reached Telegram for this long is a
	// problem; default 5m.
	BotStaleAfter Duration `toml:"bot_stale_after"`
	// RemindEvery repeats alerts for ongoing problems; default 6h.
	RemindEvery Duration       `toml:"remind_every"`
	Probes      []MonitorProbe `toml:"probes"`
}

// MonitorProbe is an HTTP URL the gateway fetches (as its service user)
// for health checks, e.g. a tunnel endpoint.
type MonitorProbe struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
	// DownAfter is how long it may fail before an alert; default 0.
	DownAfter Duration `toml:"down_after"`
}

// OpenClawCompat configures `ganclaw openclaw-compat`.
type OpenClawCompat struct {
	// FallbackBin is the real openclaw CLI, used for bots and agents
	// ganclaw doesn't know yet. Empty disables the fallback.
	FallbackBin string `toml:"fallback_bin"`
	// DefaultAgent handles `system event` calls without a session key;
	// default "main".
	DefaultAgent string `toml:"default_agent"`
	// Notify is the contact that receives replies to system events whose
	// session key doesn't name a chat.
	Notify string `toml:"notify"`
}

type Codex struct {
	Bin     string `toml:"bin"`
	Home    string `toml:"home"`
	Model   string `toml:"model"`
	Sandbox string `toml:"sandbox"`
	// WebSearch is codex's web_search mode: live (default), cached or
	// disabled.
	WebSearch string `toml:"web_search"`
}

type Claude struct {
	Bin            string   `toml:"bin"`
	ConfigDir      string   `toml:"config_dir"`
	Model          string   `toml:"model"`
	PermissionMode string   `toml:"permission_mode"`
	ExtraArgs      []string `toml:"extra_args"`
	// AllowedTools run without a permission prompt; default WebSearch and
	// WebFetch. Tools from [[mcp_servers]] are always allowed.
	AllowedTools []string `toml:"allowed_tools"`
}

// MCPServer is a stdio MCP server given to every agent, under both codex
// and claude. Its command, args and env appear in the process list: keep
// secrets out of them.
type MCPServer struct {
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	// ToolTimeoutSec bounds each tool call under codex; 0 = codex default.
	ToolTimeoutSec int `toml:"tool_timeout_sec"`
	// Description tells agents when to use this server; it is added to
	// every agent's instructions.
	Description string `toml:"description"`
}

// Transcribe configures local voice-note transcription with whisper.cpp.
// Disabled unless WhisperBin is set.
type Transcribe struct {
	WhisperBin string `toml:"whisper_bin"`
	Model      string `toml:"model"`
	Language   string `toml:"language"`
	FFmpegBin  string `toml:"ffmpeg_bin"`
	Threads    int    `toml:"threads"`
}

type Agent struct {
	Name      string `toml:"name"`
	Workspace string `toml:"workspace"`
	// InstructionFiles are read from the workspace, in order, and sent as
	// the system prompt when a conversation starts. Missing files are
	// skipped.
	InstructionFiles []string `toml:"instruction_files"`
	// Per-agent provider options; empty uses the [codex]/[claude] values.
	CodexModel     string `toml:"codex_model"`
	Sandbox        string `toml:"sandbox"`
	ClaudeModel    string `toml:"claude_model"`
	PermissionMode string `toml:"permission_mode"`
}

type Telegram struct {
	Name  string `toml:"name"`
	Agent string `toml:"agent"`
	// TokenEnv names the environment variable holding the bot token.
	TokenEnv string `toml:"token_env"`
	// AllowUsers are the Telegram user IDs allowed to talk to this bot.
	// Everyone else is ignored. Required.
	AllowUsers []int64 `toml:"allow_users"`
}

// Duration parses TOML strings like "15m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

var DefaultInstructionFiles = []string{"IDENTITY.md", "SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "MEMORY.md"}

// Load reads and validates a config file, applying defaults.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, len(un))
		for i, k := range un {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if len(c.Providers) == 0 {
		c.Providers = []string{"codex", "claude"}
	}
	if c.TurnTimeout.Duration == 0 {
		c.TurnTimeout.Duration = 20 * time.Minute
	}
	if c.AttachmentRetention.Duration == 0 {
		c.AttachmentRetention.Duration = 7 * 24 * time.Hour
	}
	if c.Monitor.BotStaleAfter.Duration == 0 {
		c.Monitor.BotStaleAfter.Duration = 5 * time.Minute
	}
	if c.Monitor.RemindEvery.Duration == 0 {
		c.Monitor.RemindEvery.Duration = 6 * time.Hour
	}
	if c.OpenClawCompat.DefaultAgent == "" {
		c.OpenClawCompat.DefaultAgent = "main"
	}
	if c.Codex.WebSearch == "" {
		c.Codex.WebSearch = "live"
	}
	if c.Claude.AllowedTools == nil {
		c.Claude.AllowedTools = []string{"WebSearch", "WebFetch"}
	}
	if c.APISocket == "" && c.StateDir != "" {
		c.APISocket = filepath.Join(c.StateDir, "ganclaw.sock")
	}
	for i := range c.Agents {
		if c.Agents[i].InstructionFiles == nil {
			c.Agents[i].InstructionFiles = DefaultInstructionFiles
		}
	}
}

// Validate checks structure only; it does not touch the filesystem or
// environment (see Telegram.Token).
func (c *Config) Validate() error {
	var errs []error
	if c.StateDir == "" {
		errs = append(errs, errors.New("state_dir is required"))
	}
	for _, p := range c.Providers {
		if p != "codex" && p != "claude" {
			errs = append(errs, fmt.Errorf("providers: unknown provider %q", p))
		}
	}
	if c.Transcribe.WhisperBin != "" && c.Transcribe.Model == "" {
		errs = append(errs, errors.New("transcribe: model is required when whisper_bin is set"))
	}
	agents := map[string]bool{}
	for i, a := range c.Agents {
		switch {
		case a.Name == "":
			errs = append(errs, fmt.Errorf("agents[%d]: name is required", i))
		case agents[a.Name]:
			errs = append(errs, fmt.Errorf("agents: duplicate name %q", a.Name))
		}
		if a.Workspace == "" {
			errs = append(errs, fmt.Errorf("agent %q: workspace is required", a.Name))
		}
		agents[a.Name] = true
	}
	bots := map[string]bool{}
	for i, t := range c.Telegram {
		switch {
		case t.Name == "":
			errs = append(errs, fmt.Errorf("telegram[%d]: name is required", i))
		case bots[t.Name]:
			errs = append(errs, fmt.Errorf("telegram: duplicate name %q", t.Name))
		}
		bots[t.Name] = true
		if !agents[t.Agent] {
			errs = append(errs, fmt.Errorf("telegram %q: unknown agent %q", t.Name, t.Agent))
		}
		if t.TokenEnv == "" {
			errs = append(errs, fmt.Errorf("telegram %q: token_env is required", t.Name))
		}
		if len(t.AllowUsers) == 0 {
			errs = append(errs, fmt.Errorf("telegram %q: allow_users is required (bots can run commands; refusing to start an open bot)", t.Name))
		}
	}
	contacts := map[string]bool{}
	for i, ct := range c.Contacts {
		switch {
		case ct.Name == "":
			errs = append(errs, fmt.Errorf("contacts[%d]: name is required", i))
		case contacts[ct.Name]:
			errs = append(errs, fmt.Errorf("contacts: duplicate name %q", ct.Name))
		}
		contacts[ct.Name] = true
		if ct.DefaultBot != "" && !bots[ct.DefaultBot] {
			errs = append(errs, fmt.Errorf("contact %q: unknown default_bot %q", ct.Name, ct.DefaultBot))
		}
	}
	switch c.Codex.WebSearch {
	case "live", "cached", "disabled":
	default:
		errs = append(errs, fmt.Errorf("codex: web_search must be live, cached or disabled, not %q", c.Codex.WebSearch))
	}
	for _, a := range c.Agents {
		switch a.Sandbox {
		case "", "read-only", "workspace-write", "danger-full-access":
		default:
			errs = append(errs, fmt.Errorf("agent %q: sandbox must be read-only, workspace-write or danger-full-access", a.Name))
		}
	}
	servers := map[string]bool{}
	for i, m := range c.MCPServers {
		switch {
		case !validMCPName(m.Name):
			errs = append(errs, fmt.Errorf("mcp_servers[%d]: name must be letters, digits, - or _", i))
		case servers[m.Name]:
			errs = append(errs, fmt.Errorf("mcp_servers: duplicate name %q", m.Name))
		}
		servers[m.Name] = true
		if m.Command == "" {
			errs = append(errs, fmt.Errorf("mcp server %q: command is required", m.Name))
		}
	}
	if n := c.Monitor.Notify; n != "" && !contacts[n] {
		errs = append(errs, fmt.Errorf("monitor: notify contact %q is not defined", n))
	}
	if v := c.Monitor.Via; v != "" && !bots[v] {
		errs = append(errs, fmt.Errorf("monitor: unknown via bot %q", v))
	}
	for i, p := range c.Monitor.Probes {
		if p.Name == "" || p.URL == "" {
			errs = append(errs, fmt.Errorf("monitor.probes[%d]: name and url are required", i))
		}
	}
	if n := c.OpenClawCompat.Notify; n != "" && !contacts[n] {
		errs = append(errs, fmt.Errorf("openclaw_compat: notify contact %q is not defined", n))
	}
	return errors.Join(errs...)
}

// Contact returns the named contact.
func (c *Config) Contact(name string) (Contact, bool) {
	i := slices.IndexFunc(c.Contacts, func(ct Contact) bool { return ct.Name == name })
	if i < 0 {
		return Contact{}, false
	}
	return c.Contacts[i], true
}

// Agent returns the named agent.
func (c *Config) Agent(name string) (Agent, bool) {
	i := slices.IndexFunc(c.Agents, func(a Agent) bool { return a.Name == name })
	if i < 0 {
		return Agent{}, false
	}
	return c.Agents[i], true
}

// Token reads the bot token from its environment variable.
func (t Telegram) Token(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	v := strings.TrimSpace(getenv(t.TokenEnv))
	if v == "" {
		return "", fmt.Errorf("telegram %q: environment variable %s is empty", t.Name, t.TokenEnv)
	}
	return v, nil
}

func validMCPName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
