package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/config"
)

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlStrings(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// codexOverrides turns config into codex `-c key=value` settings.
func codexOverrides(cfg *config.Config, servers []config.MCPServer) []string {
	out := []string{"web_search=" + tomlString(cfg.Codex.WebSearch)}
	for _, m := range servers {
		p := "mcp_servers." + m.Name + "."
		out = append(out, p+"command="+tomlString(m.Command))
		if len(m.Args) > 0 {
			out = append(out, p+"args="+tomlStrings(m.Args))
		}
		if len(m.Env) > 0 {
			keys := make([]string, 0, len(m.Env))
			for k := range m.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			kv := make([]string, len(keys))
			for i, k := range keys {
				kv[i] = tomlString(k) + " = " + tomlString(m.Env[k])
			}
			out = append(out, p+"env={"+strings.Join(kv, ", ")+"}")
		}
		if m.ToolTimeoutSec > 0 {
			out = append(out, fmt.Sprintf("%stool_timeout_sec=%d", p, m.ToolTimeoutSec))
		}
	}
	return out
}

// writeClaudeMCPConfig writes the servers in claude's --mcp-config format.
func writeClaudeMCPConfig(path string, servers []config.MCPServer) error {
	type server struct {
		Command string            `json:"command"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}
	doc := struct {
		MCPServers map[string]server `json:"mcpServers"`
	}{MCPServers: map[string]server{}}
	for _, m := range servers {
		doc.MCPServers[m.Name] = server{Command: m.Command, Args: m.Args, Env: m.Env}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// claudeAllowedTools is the configured list plus every MCP server's tools.
func claudeAllowedTools(cfg *config.Config, servers []config.MCPServer) []string {
	tools := append([]string(nil), cfg.Claude.AllowedTools...)
	for _, m := range servers {
		tools = append(tools, "mcp__"+m.Name)
	}
	return tools
}

// mcpServers is the configured servers plus ganclaw's own agent tools.
func mcpServers(cfg *config.Config) ([]config.MCPServer, error) {
	servers := append([]config.MCPServer(nil), cfg.MCPServers...)
	if cfg.DisableAgentTools {
		return servers, nil
	}
	for _, m := range servers {
		if m.Name == "ganclaw" {
			return nil, fmt.Errorf("mcp server name %q is reserved for ganclaw's agent tools (set disable_agent_tools to use it)", m.Name)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return append(servers, config.MCPServer{
		Name:    "ganclaw",
		Command: exe,
		Args:    []string{"mcp"},
		Env:     map[string]string{"GANCLAW_SOCKET": cfg.APISocket},
	}), nil
}

func contactNames(cs []config.Contact) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

// toolsAppendix describes the MCP servers that have descriptions, for
// agents' instructions. Empty if none do.
func toolsAppendix(cfg *config.Config) string {
	var lines []string
	for _, m := range cfg.MCPServers {
		if m.Description != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", m.Name, m.Description))
		}
	}
	if !cfg.DisableAgentTools {
		lines = append(lines, "- ganclaw: message a person (send_message), ask another agent and get its answer (ask_agent), hand a task to another agent whose reply goes to a person (hand_off), list agents and contacts (list_agents).")
	}
	if len(lines) == 0 {
		return ""
	}
	return "# Tool servers (provided by ganclaw)\n\n" + strings.Join(lines, "\n")
}
