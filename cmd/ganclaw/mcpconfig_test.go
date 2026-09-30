package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/config"
)

var browserServer = config.MCPServer{
	Name:           "browser",
	Command:        "/usr/local/lib/ganclaw/node/bin/node",
	Args:           []string{"/opt/mcp/cli.js", "--headless", "--isolated"},
	Env:            map[string]string{"PLAYWRIGHT_BROWSERS_PATH": "/opt/ms-playwright", "A": "x\"y"},
	ToolTimeoutSec: 120,
}

func TestTomlString(t *testing.T) {
	subject := tomlString

	t.Run("Escapes quotes, backslashes and control characters", func(t *testing.T) {
		assert.Equal(t, `"plain"`, subject("plain"))
		assert.Equal(t, `"say \"hi\"\\n\n\t\u0001"`, subject("say \"hi\"\\n\n\t\x01"))
		assert.Equal(t, `"café"`, subject("café"))
	})
}

func TestCodexOverrides(t *testing.T) {
	subject := codexOverrides

	t.Run("Web search and MCP servers become -c settings", func(t *testing.T) {
		cfg := &config.Config{Codex: config.Codex{WebSearch: "live"}}

		got := subject(cfg, []config.MCPServer{browserServer, {Name: "tiny", Command: "tiny-mcp"}})

		assert.Equal(t, []string{
			`web_search="live"`,
			`mcp_servers.browser.command="/usr/local/lib/ganclaw/node/bin/node"`,
			`mcp_servers.browser.args=["/opt/mcp/cli.js", "--headless", "--isolated"]`,
			`mcp_servers.browser.env={"A" = "x\"y", "PLAYWRIGHT_BROWSERS_PATH" = "/opt/ms-playwright"}`,
			`mcp_servers.browser.tool_timeout_sec=120`,
			`mcp_servers.tiny.command="tiny-mcp"`,
		}, got)
	})
}

func TestWriteClaudeMCPConfig(t *testing.T) {
	subject := writeClaudeMCPConfig

	t.Run("Writes a private mcpServers file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "claude-mcp.json")

		require.NoError(t, subject(path, []config.MCPServer{browserServer}))

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		var doc map[string]map[string]map[string]any
		data, _ := os.ReadFile(path)
		require.NoError(t, json.Unmarshal(data, &doc))
		b := doc["mcpServers"]["browser"]
		assert.Equal(t, "/usr/local/lib/ganclaw/node/bin/node", b["command"])
		assert.Equal(t, []any{"/opt/mcp/cli.js", "--headless", "--isolated"}, b["args"])
		assert.Equal(t, "/opt/ms-playwright", b["env"].(map[string]any)["PLAYWRIGHT_BROWSERS_PATH"])
	})
}

func TestToolsAppendix(t *testing.T) {
	subject := toolsAppendix

	t.Run("Lists described servers and the agent tools", func(t *testing.T) {
		got := subject(&config.Config{MCPServers: []config.MCPServer{
			{Name: "browser", Description: "headless, logged out"},
			{Name: "quiet"},
		}})

		assert.Equal(t, "# Tool servers (provided by ganclaw)\n\n- browser: headless, logged out\n- ganclaw: message a person (send_message), ask another agent and get its answer (ask_agent), hand a task to another agent whose reply goes to a person (hand_off), list agents and contacts (list_agents).", got)
	})

	t.Run("Nothing to describe", func(t *testing.T) {
		assert.Empty(t, subject(&config.Config{DisableAgentTools: true, MCPServers: []config.MCPServer{{Name: "quiet"}}}))
	})
}

func TestMCPServers(t *testing.T) {
	subject := mcpServers

	t.Run("Adds ganclaw's agent tools with the socket", func(t *testing.T) {
		got, err := subject(&config.Config{APISocket: "/state/ganclaw.sock", MCPServers: []config.MCPServer{browserServer}})

		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "ganclaw", got[1].Name)
		assert.Equal(t, []string{"mcp"}, got[1].Args)
		assert.Equal(t, map[string]string{"GANCLAW_SOCKET": "/state/ganclaw.sock"}, got[1].Env)
	})

	t.Run("Can be disabled, and the name is reserved", func(t *testing.T) {
		got, err := subject(&config.Config{DisableAgentTools: true, MCPServers: []config.MCPServer{browserServer}})
		require.NoError(t, err)
		assert.Len(t, got, 1)

		_, err = subject(&config.Config{MCPServers: []config.MCPServer{{Name: "ganclaw", Command: "x"}}})
		assert.ErrorContains(t, err, "reserved")
	})

	t.Run("Claude may use every server's tools", func(t *testing.T) {
		cfg := &config.Config{Claude: config.Claude{AllowedTools: []string{"WebSearch", "WebFetch"}}}

		assert.Equal(t, []string{"WebSearch", "WebFetch", "mcp__browser", "mcp__ganclaw"},
			claudeAllowedTools(cfg, []config.MCPServer{browserServer, {Name: "ganclaw"}}))
	})
}
