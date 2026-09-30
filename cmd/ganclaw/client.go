package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

// Exit codes for client commands, so schedulers can decide what to retry.
const (
	exitFailed      = 1
	exitUsage       = 2
	exitUnavailable = 3 // all providers unavailable: retry later
	exitBadAddress  = 4 // unknown contact/bot/agent or bad request: don't retry
	exitTimeout     = 5
	exitBusy        = 6 // queue full or same idempotency key running: retry soon
	exitNoServer    = 7 // ganclaw isn't running
)

// exitError carries a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case api.CodeUnavailable:
			return exitUnavailable
		case api.CodeUnknownContact, api.CodeUnknownBot, api.CodeUnknownAgent, api.CodeBadRequest:
			return exitBadAddress
		case api.CodeTimeout:
			return exitTimeout
		case api.CodeBusy, api.CodeInProgress:
			return exitBusy
		case api.CodeNoServer:
			return exitNoServer
		}
	}
	return exitFailed
}

// defaultConfigPath is where client commands look for the socket setting.
func defaultConfigPath() string {
	if _, err := os.Stat("/etc/ganclaw/ganclaw.toml"); err == nil {
		return "/etc/ganclaw/ganclaw.toml"
	}
	return "ganclaw.toml"
}

// socketPath resolves the API socket: -socket, then $GANCLAW_SOCKET, then
// api_socket from the config file.
func socketPath(flagSocket, configPath string) (string, error) {
	if flagSocket != "" {
		return flagSocket, nil
	}
	if s := os.Getenv("GANCLAW_SOCKET"); s != "" {
		return s, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("finding the API socket (set -socket or GANCLAW_SOCKET): %w", err)
	}
	return cfg.APISocket, nil
}

type clientFlags struct {
	socket *string
	config *string
	json   *bool
}

func addClientFlags(fs *flag.FlagSet) clientFlags {
	return clientFlags{
		socket: fs.String("socket", "", "API socket (default: $GANCLAW_SOCKET or api_socket from -config)"),
		config: fs.String("config", defaultConfigPath(), "config file used to find the API socket"),
		json:   fs.Bool("json", false, "print the JSON response"),
	}
}

func (f clientFlags) client() (*api.Client, error) {
	s, err := socketPath(*f.socket, *f.config)
	if err != nil {
		return nil, &exitError{code: exitUsage, err: err}
	}
	return api.NewClient(s), nil
}

// textArg returns the positional text, or stdin when it is "-" or absent.
func textArg(args []string, stdin io.Reader) (string, error) {
	if len(args) == 1 && args[0] == "-" || len(args) == 0 {
		b, err := io.ReadAll(stdin)
		return strings.TrimRight(string(b), "\n"), err
	}
	return strings.Join(args, " "), nil
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// cmdSend: ganclaw send --to alex [--via bot] "text"
func cmdSend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	to := fs.String("to", "", "contact name or telegram:<bot>:<chat> (required)")
	via := fs.String("via", "", "bot to send through (overrides the contact's default_bot)")
	key := fs.String("idempotency-key", "", "skip if a send with this key already succeeded")
	cf := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	text, err := textArg(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	c, err := cf.client()
	if err != nil {
		return err
	}
	resp, err := c.Send(ctx, api.SendRequest{To: *to, Via: *via, Text: text, IdempotencyKey: *key})
	if err != nil {
		return err
	}
	if *cf.json {
		printJSON(os.Stdout, resp)
	}
	return nil
}

// cmdRunAgent: ganclaw run --agent a [--to alex] [--session s] "prompt"
func cmdRunAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	agentName := fs.String("agent", "", "agent to run (required)")
	to := fs.String("to", "", "deliver the reply to this contact or address and continue that chat")
	via := fs.String("via", "", "bot to deliver through")
	session := fs.String("session", "", "named conversation for undelivered runs (default: one-off)")
	promptFile := fs.String("prompt-file", "", "read the prompt from this file")
	timeout := fs.Duration("timeout", 0, "give up after this long (e.g. 10m)")
	key := fs.String("idempotency-key", "", "skip if a run with this key already succeeded")
	cf := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	var prompt string
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			return &exitError{code: exitUsage, err: err}
		}
		prompt = string(b)
	} else {
		var err error
		if prompt, err = textArg(fs.Args(), os.Stdin); err != nil {
			return err
		}
	}
	c, err := cf.client()
	if err != nil {
		return err
	}
	resp, err := c.Run(ctx, api.RunRequest{
		Agent: *agentName, Prompt: prompt, To: *to, Via: *via, Session: *session,
		TimeoutSeconds: int(timeout.Round(time.Second) / time.Second), IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	if *cf.json {
		printJSON(os.Stdout, resp)
	} else if *to == "" {
		fmt.Println(resp.Text)
	}
	return nil
}

// cmdStatus: provider availability from the running gateway.
func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	cf := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	c, err := cf.client()
	if err != nil {
		return err
	}
	statuses, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if *cf.json {
		printJSON(os.Stdout, statuses)
		return nil
	}
	fmt.Println(provider.FormatStatus(statuses, time.Now()))
	return nil
}
