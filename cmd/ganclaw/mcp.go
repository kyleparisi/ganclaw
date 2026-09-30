package main

import (
	"context"
	"flag"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kyleparisi/ganclaw/internal/agenttools"
)

// cmdMCP serves ganclaw's agent tools over stdio; codex and claude start it.
func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	cf := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	c, err := cf.client()
	if err != nil {
		return err
	}
	s := agenttools.NewServer(agenttools.Deps{Send: c.Send, Run: c.Run, Agents: c.Agents}, version)
	return s.Run(ctx, &mcp.StdioTransport{})
}
