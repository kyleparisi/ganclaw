package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/attachments"
	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/gateway"
	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/provider/claude"
	"github.com/kyleparisi/ganclaw/internal/provider/codex"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/kyleparisi/ganclaw/internal/telegram"
	"github.com/kyleparisi/ganclaw/internal/transcribe"
)

// cmdServe starts every configured bot and the API, and serves until
// interrupted.
func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	path := fs.String("config", "ganclaw.toml", "config file")
	fs.Parse(args)

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	log, err := newLogger(os.Stderr, cfg.LogLevel)
	if err != nil {
		return err
	}
	if len(cfg.Telegram) == 0 {
		return errors.New("no bots configured")
	}

	// Resolve every token before starting anything.
	tokens := map[string]string{}
	for _, t := range cfg.Telegram {
		tok, err := t.Token(os.Getenv)
		if err != nil {
			return err
		}
		tokens[t.Name] = tok
	}

	st, err := store.Open(filepath.Join(cfg.StateDir, "ganclaw.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	chain, closeChain, err := buildChain(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeChain()

	appendix := mediaAppendix
	if tools := toolsAppendix(cfg); tools != "" {
		appendix = tools + "\n\n" + mediaAppendix
	}
	agents := map[string]agent.Agent{}
	for _, a := range cfg.Agents {
		if fi, err := os.Stat(a.Workspace); err != nil || !fi.IsDir() {
			return fmt.Errorf("agent %q: workspace %s is not a directory", a.Name, a.Workspace)
		}
		agents[a.Name] = agent.Agent{
			Name: a.Name, Workspace: a.Workspace, InstructionFiles: a.InstructionFiles,
			Appendix: appendix,
			Settings: provider.Settings{
				CodexModel: a.CodexModel, CodexSandbox: a.Sandbox,
				ClaudeModel: a.ClaudeModel, PermissionMode: a.PermissionMode,
			},
		}
	}

	var transcriber func(context.Context, string) (string, error)
	if tc := cfg.Transcribe; tc.WhisperBin != "" {
		transcriber = transcribe.New(transcribe.Config{
			Whisper:  tc.WhisperBin,
			Model:    tc.Model,
			Language: tc.Language,
			FFmpeg:   tc.FFmpegBin,
			Threads:  tc.Threads,
			Logger:   log,
		}).Transcribe
		log.Info("voice transcription enabled", "model", filepath.Base(tc.Model))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := router.New(ctx, router.Config{
		Agents:      agents,
		Store:       st,
		Run:         chain.Run,
		Status:      chain.Status,
		Transcribe:  transcriber,
		TurnTimeout: cfg.TurnTimeout.Duration,
		Logger:      log,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		housekeeping(ctx, log, st, agents, cfg.AttachmentRetention.Duration)
	}()

	errc := make(chan error, len(cfg.Telegram)+1)
	bots := map[string]*telegram.Bot{}
	botNames := make([]string, 0, len(cfg.Telegram))
	for _, t := range cfg.Telegram {
		bot := &telegram.Bot{
			Name:       t.Name,
			Agent:      t.Agent,
			Client:     &telegram.Client{Token: tokens[t.Name]},
			AllowUsers: t.AllowUsers,
			Store:      st,
			Submit:     r.Submit,
			Commands:   router.Commands,
			Logger:     log,
		}
		bots[t.Name] = bot
		botNames = append(botNames, t.Name)
	}
	for _, bot := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bot.Run(ctx); err != nil {
				errc <- err
				cancel()
			}
		}()
	}

	gw := &gateway.Gateway{Ctx: ctx, Router: r, Agents: agents, Bots: bots, Logger: log}
	apiSrv := &api.Server{
		Resolve:  api.Resolver{Contacts: cfg.Contacts, Bots: botNames}.Resolve,
		Send:     gw.Send,
		Run:      gw.Run,
		Status:   chain.Status,
		AgentBot: gw.AgentBot,
		Agents:   gw.ListAgents(contactNames(cfg.Contacts)),
		Health:   gw.Health(version, chain.Status, monitorProbes(cfg), nil),
		Store:    st,
		Logger:   log,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := apiSrv.Serve(ctx, cfg.APISocket); err != nil {
			errc <- err
			cancel()
		}
	}()

	log.Info("ganclaw running", "version", version, "bots", len(cfg.Telegram), "agents", len(agents), "providers", cfg.Providers)
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait()
	r.Wait()
	close(errc)
	return errors.Join(collect(errc)...)
}

func collect(errc <-chan error) []error {
	var errs []error
	for err := range errc {
		errs = append(errs, err)
	}
	return errs
}

// buildChain constructs providers in the configured fallback order. The
// returned func closes them.
func buildChain(ctx context.Context, cfg *config.Config, log *slog.Logger) (*provider.Chain, func(), error) {
	chain := &provider.Chain{Logger: log}
	servers, err := mcpServers(cfg)
	if err != nil {
		return nil, nil, err
	}
	var claudeMCP string
	if len(servers) > 0 {
		claudeMCP = filepath.Join(cfg.StateDir, "claude-mcp.json")
		if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
			return nil, nil, err
		}
		if err := writeClaudeMCPConfig(claudeMCP, servers); err != nil {
			return nil, nil, err
		}
		names := make([]string, len(servers))
		for i, m := range servers {
			names[i] = m.Name
		}
		log.Info("mcp servers for agents", "servers", names)
	}
	// Providers (and every shell command their agents run) inherit this
	// environment: never pass ganclaw's own secrets, and give each
	// provider only its own credentials.
	codexEnv := func() []string {
		return procexec.FilterEnv(os.Environ(), func(k string) bool {
			return isGanclawVar(k) || k == "CLAUDE_CODE_OAUTH_TOKEN"
		})
	}
	claudeEnv := func() []string { return procexec.FilterEnv(os.Environ(), isGanclawVar) }
	closeAll := func() {
		for _, p := range chain.Providers {
			p.Close()
		}
	}
	for _, name := range cfg.Providers {
		switch name {
		case "codex":
			p, err := codex.New(ctx, codex.Config{
				Bin:           cfg.Codex.Bin,
				CodexHome:     cfg.Codex.Home,
				Model:         cfg.Codex.Model,
				Sandbox:       cfg.Codex.Sandbox,
				Overrides:     codexOverrides(cfg, servers),
				ClientVersion: version,
				Environ:       codexEnv,
				Logger:        log,
			})
			if err != nil {
				closeAll()
				return nil, nil, err
			}
			chain.Providers = append(chain.Providers, p)
		case "claude":
			chain.Providers = append(chain.Providers, claude.New(claude.Config{
				Bin:            cfg.Claude.Bin,
				ConfigDir:      cfg.Claude.ConfigDir,
				Model:          cfg.Claude.Model,
				PermissionMode: cfg.Claude.PermissionMode,
				ExtraArgs:      cfg.Claude.ExtraArgs,
				MCPConfig:      claudeMCP,
				AllowedTools:   claudeAllowedTools(cfg, servers),
				Environ:        claudeEnv,
				Logger:         log,
			}))
		}
	}
	return chain, closeAll, nil
}

func isGanclawVar(k string) bool { return strings.HasPrefix(k, "GANCLAW_") }

// cmdHealth prints each provider's login/limit status and exits non-zero
// if no provider is available.
func cmdHealth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	path := fs.String("config", "ganclaw.toml", "config file")
	fs.Parse(args)

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	log, err := newLogger(os.Stderr, "warn")
	if err != nil {
		return err
	}
	chain, closeChain, err := buildChain(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeChain()

	statuses := chain.Status(ctx)
	fmt.Println(provider.FormatStatus(statuses, time.Now()))
	for _, s := range statuses {
		if s.Available {
			return nil
		}
	}
	return errors.New("no provider is available")
}

// housekeeping deletes old attachments and idempotency keys at startup
// and then daily.
func housekeeping(ctx context.Context, log *slog.Logger, st *store.Store, agents map[string]agent.Agent, retention time.Duration) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if n, err := st.PruneIdempotency(ctx, 7*24*time.Hour); err != nil {
			log.Warn("prune idempotency keys", "err", err)
		} else if n > 0 {
			log.Info("old idempotency keys removed", "count", n)
		}
		for _, a := range agents {
			n, err := attachments.Sweep(attachments.Dir(a.Workspace), retention, time.Now())
			if err != nil {
				log.Warn("sweep attachments", "agent", a.Name, "err", err)
			} else if n > 0 {
				log.Info("old attachments removed", "agent", a.Name, "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
