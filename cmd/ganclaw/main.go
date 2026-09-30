// Command ganclaw is a chat gateway that routes Telegram and Slack messages
// to coding-agent CLIs (codex, claude) running as subprocesses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/provider/claude"
	"github.com/kyleparisi/ganclaw/internal/provider/codex"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Installed or symlinked as "openclaw": behave like its CLI.
	if filepath.Base(os.Args[0]) == "openclaw" {
		exit(cmdOpenClawCompat(ctx, os.Args[1:]))
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(ctx, os.Args[2:])
	case "health":
		err = cmdHealth(ctx, os.Args[2:])
	case "send":
		err = cmdSend(ctx, os.Args[2:])
	case "run":
		err = cmdRunAgent(ctx, os.Args[2:])
	case "status":
		err = cmdStatus(ctx, os.Args[2:])
	case "check":
		err = cmdCheck(ctx, os.Args[2:])
	case "mcp":
		err = cmdMCP(ctx, os.Args[2:])
	case "openclaw-compat":
		err = cmdOpenClawCompat(ctx, os.Args[2:])
	case "ask":
		err = cmdAsk(ctx, os.Args[2:])
	case "version":
		fmt.Println("ganclaw", version)
	default:
		usage()
		os.Exit(2)
	}
	exit(err)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ganclaw:", err)
		os.Exit(exitCode(err))
	}
	os.Exit(0)
}

func newLogger(w io.Writer, level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q", level)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l})), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: ganclaw <command> [flags]

gateway:
  serve             run the gateway: telegram bots, agents and the local API
  health            check each provider's login and limits directly
  check             check a running gateway and alert the monitor contact (for a timer)

client (talks to a running gateway over its socket):
  send              send a message:      ganclaw send --to alex "text"
  run               run an agent:        ganclaw run --agent a [--to alex] "prompt"
  status            provider availability from the gateway
  openclaw-compat   openclaw-style "message send" / "agent" (also when invoked as "openclaw")
  mcp               MCP server with agent tools (started by codex/claude)

other:
  ask               send one prompt straight to a provider (debugging)
  version           print version`)
}

// cmdAsk sends a single prompt through one provider or the fallback chain.
// Useful for checking logins and protocol compatibility without any chat
// channel.
func cmdAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	use := fs.String("provider", "chain", "codex | claude | chain (codex, then claude)")
	codexBin := fs.String("codex", "codex", "codex executable")
	codexHome := fs.String("codex-home", os.Getenv("CODEX_HOME"), "CODEX_HOME directory (login + thread state)")
	codexSession := fs.String("codex-session", "", "codex thread ID to resume")
	sandbox := fs.String("sandbox", "read-only", "codex sandbox: read-only | workspace-write | danger-full-access")
	claudeBin := fs.String("claude", "claude", "claude executable")
	claudeConfig := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR (login, settings, sessions); empty = ~/.claude")
	claudeSession := fs.String("claude-session", "", "claude session ID to resume")
	permission := fs.String("permission-mode", "default", "claude --permission-mode")
	cwd := fs.String("cwd", ".", "working directory for the agent")
	instructions := fs.String("instructions", "", "system instructions for a new session")
	logLevel := fs.String("log-level", "warn", "debug | info | warn | error")
	fs.Parse(args)

	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return errors.New("ask: prompt required")
	}
	log, err := newLogger(os.Stderr, *logLevel)
	if err != nil {
		return err
	}

	chain := provider.Chain{Logger: log}
	if *use == "codex" || *use == "chain" {
		cp, err := codex.New(ctx, codex.Config{
			Bin: *codexBin, CodexHome: *codexHome, Sandbox: *sandbox,
			ClientVersion: version, Logger: log,
		})
		if err != nil {
			return err
		}
		defer cp.Close()
		chain.Providers = append(chain.Providers, cp)
	}
	if *use == "claude" || *use == "chain" {
		chain.Providers = append(chain.Providers, claude.New(claude.Config{
			Bin: *claudeBin, ConfigDir: *claudeConfig, PermissionMode: *permission, Logger: log,
		}))
	}
	if len(chain.Providers) == 0 {
		return fmt.Errorf("ask: unknown provider %q", *use)
	}

	sessions := provider.Sessions{"codex": *codexSession, "claude": *claudeSession}
	used, _, err := chain.Run(ctx, sessions, provider.Request{
		Cwd:          *cwd,
		Instructions: *instructions,
		Prompt:       prompt,
		OnDelta:      func(s string) { fmt.Print(s) },
	})
	fmt.Println()
	var ce *provider.ChainError
	if errors.As(err, &ce) {
		for _, a := range ce.Attempts {
			fmt.Fprintf(os.Stderr, "%s failed: %v\n", a.Provider, a.Err)
		}
	}
	if used != "" {
		fmt.Fprintln(os.Stderr, "answered by:", used)
	}
	for name, id := range sessions {
		if id != "" {
			fmt.Fprintf(os.Stderr, "%s-session: %s\n", name, id)
		}
	}
	return err
}
