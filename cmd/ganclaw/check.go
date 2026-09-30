package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/gateway"
	"github.com/kyleparisi/ganclaw/internal/monitor"
	"github.com/kyleparisi/ganclaw/internal/telegram"
)

func monitorProbes(cfg *config.Config) []gateway.Probe {
	out := make([]gateway.Probe, len(cfg.Monitor.Probes))
	for i, p := range cfg.Monitor.Probes {
		out[i] = gateway.Probe{Name: p.Name, URL: p.URL}
	}
	return out
}

func monitorSettings(cfg *config.Config) monitor.Settings {
	grace := map[string]time.Duration{}
	for _, p := range cfg.Monitor.Probes {
		grace[p.Name] = p.DownAfter.Duration
	}
	return monitor.Settings{BotStaleAfter: cfg.Monitor.BotStaleAfter.Duration, ProbeGrace: grace, RemindEvery: cfg.Monitor.RemindEvery.Duration}
}

// telegramHTTP is used for direct alerts; tests replace it.
var telegramHTTP telegram.HTTPClient

// cmdCheck checks the running gateway and alerts the monitor contact about
// problems. Meant for a timer running as root: if the gateway is down it
// alerts through the Telegram API directly, using the token from -env.
func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	path := fs.String("config", defaultConfigPath(), "config file")
	envFile := fs.String("env", "/etc/ganclaw/ganclaw.env", "environment file with bot tokens (for alerting when ganclaw is down)")
	statePath := fs.String("state", "/var/lib/ganclaw/check-state.json", "where alert state is kept between runs")
	dryRun := fs.Bool("dry-run", false, "print what would be sent without sending or saving state")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	if cfg.Monitor.Notify == "" {
		return &exitError{code: exitUsage, err: errors.New("check: set monitor.notify to the contact that should receive alerts")}
	}

	client := api.NewClient(cfg.APISocket)
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	health, apiErr := client.Health(hctx)
	cancel()

	settings := monitorSettings(cfg)
	now := time.Now()
	obs := monitor.Observe(health, apiErr, now, settings)
	st, err := monitor.Load(*statePath)
	if err != nil {
		return err
	}
	next, lines := monitor.Step(st, obs, now, settings)

	fmt.Printf("%d problem(s) now, %d message line(s)\n", len(obs), len(lines))
	if len(lines) > 0 {
		text := "ganclaw health\n" + strings.Join(lines, "\n")
		if *dryRun {
			fmt.Println(text)
			return nil
		}
		if err := alert(ctx, cfg, client, apiErr == nil, *envFile, text); err != nil {
			return fmt.Errorf("check: could not send alert (will retry next run): %w", err)
		}
	}
	if *dryRun {
		return nil
	}
	return monitor.Save(*statePath, next)
}

// alert sends through the gateway when it's up, otherwise straight to
// Telegram with the notify bot's token.
func alert(ctx context.Context, cfg *config.Config, client *api.Client, gatewayUp bool, envFile, text string) error {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if gatewayUp {
		_, err := client.Send(sctx, api.SendRequest{To: cfg.Monitor.Notify, Via: cfg.Monitor.Via, Text: text})
		if err == nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "check: sending through ganclaw failed, trying Telegram directly:", err)
	}

	contact, _ := cfg.Contact(cfg.Monitor.Notify)
	botName := cfg.Monitor.Via
	if botName == "" {
		botName = contact.DefaultBot
	}
	var tokenEnv string
	for _, t := range cfg.Telegram {
		if t.Name == botName {
			tokenEnv = t.TokenEnv
		}
	}
	if tokenEnv == "" || contact.TelegramChat == 0 {
		return fmt.Errorf("no bot/chat to alert %q directly (set monitor.via and the contact's telegram_chat)", cfg.Monitor.Notify)
	}
	env, err := readEnvFile(envFile)
	if err != nil {
		return err
	}
	token := env[tokenEnv]
	if token == "" {
		return fmt.Errorf("%s not found in %s", tokenEnv, envFile)
	}
	return (&telegram.Client{Token: token, HTTP: telegramHTTP}).SendMessage(sctx, contact.TelegramChat, text)
}

// readEnvFile parses KEY=VALUE lines (systemd EnvironmentFile style).
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, sc.Err()
}
