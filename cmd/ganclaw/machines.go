package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/config"
)

// machinesAppendix makes remote computers discoverable to every agent.
func machinesAppendix(cfg *config.Config) string {
	if len(cfg.Machines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Remote machines (provided by ganclaw)\n\nUse your terminal tool for SSH commands and the named browser tools for browser control. If a machine is offline, report it instead of repeatedly retrying. SSH access does not change your tool permissions.\n")
	for _, m := range cfg.Machines {
		fmt.Fprintf(&b, "\n## %s\n", m.Name)
		if m.Description != "" {
			fmt.Fprintf(&b, "\n%s\n", m.Description)
		}
		if len(m.Capabilities) > 0 {
			fmt.Fprintf(&b, "\nCapabilities: %s.\n", strings.Join(m.Capabilities, ", "))
		}
		if m.BrowserServer != "" {
			fmt.Fprintf(&b, "\nBrowser MCP server: %s.\n", m.BrowserServer)
		}
		if s := m.SSH; s.Host != "" {
			args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"}
			if s.Port != 0 {
				args = append(args, "-p", strconv.Itoa(s.Port))
			}
			if s.User != "" {
				args = append(args, "-l", shellArgument(s.User))
			}
			if s.IdentityFile != "" {
				args = append(args, "-o", "IdentitiesOnly=yes", "-i", shellArgument(s.IdentityFile))
			}
			args = append(args, shellArgument(s.Host))
			fmt.Fprintf(&b, "\nSSH command prefix (append a quoted remote command):\n\n```sh\n%s\n```\n", strings.Join(args, " "))
		}
	}
	return strings.TrimSpace(b.String())
}

func shellArgument(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
