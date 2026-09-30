package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
)

// compatCmd is a parsed `openclaw message send`, `agent` or `system event`
// call.
type compatCmd struct {
	Kind        string // "send", "agent" or "event"
	Channel     string
	Account     string
	Target      string
	Message     string
	MessageFile string
	Agent       string
	SessionKey  string
	Deliver     bool
	ReplyChan   string
	ReplyAcct   string
	ReplyTo     string
	Timeout     int // seconds for agent, milliseconds for event
	JSON        bool
	Text        string // event
	Mode        string // event: now | next-heartbeat
	ExpectFinal bool   // event: wait for the agent to finish
}

var errUnsupported = errors.New("unsupported openclaw command")

// parseCompat understands the subset of the openclaw CLI that scripts use.
func parseCompat(args []string) (compatCmd, error) {
	var c compatCmd
	var rest []string
	switch {
	case len(args) >= 2 && args[0] == "message" && args[1] == "send":
		c.Kind, rest = "send", args[2:]
	case len(args) >= 1 && args[0] == "agent":
		c.Kind, rest = "agent", args[1:]
	case len(args) >= 2 && args[0] == "system" && args[1] == "event":
		c.Kind, rest = "event", args[2:]
	default:
		return c, fmt.Errorf("%w: %s", errUnsupported, strings.Join(args, " "))
	}
	str := map[string]*string{
		"--channel": &c.Channel, "--account": &c.Account, "--target": &c.Target,
		"--message": &c.Message, "--message-file": &c.MessageFile, "--agent": &c.Agent,
		"--session-key": &c.SessionKey, "--reply-channel": &c.ReplyChan,
		"--reply-account": &c.ReplyAcct, "--reply-to": &c.ReplyTo,
		"--text": &c.Text, "--mode": &c.Mode,
		// accepted and ignored: ganclaw has no per-call thinking level or
		// remote gateway settings
		"--thinking": new(string), "--port": new(string), "--url": new(string),
		"--token": new(string), "--password": new(string),
	}
	for i := 0; i < len(rest); i++ {
		name, val, hasVal := strings.Cut(rest[i], "=")
		switch name {
		case "--json":
			c.JSON = true
			continue
		case "--deliver":
			c.Deliver = true
			continue
		case "--expect-final":
			c.ExpectFinal = true
			continue
		}
		if !hasVal {
			if i+1 >= len(rest) {
				return c, fmt.Errorf("%w: %s needs a value", errUnsupported, name)
			}
			i++
			val = rest[i]
		}
		if name == "--timeout" {
			n, err := strconv.Atoi(val)
			if err != nil {
				return c, fmt.Errorf("%w: --timeout %q", errUnsupported, val)
			}
			c.Timeout = n
			continue
		}
		p, ok := str[name]
		if !ok {
			return c, fmt.Errorf("%w: flag %s", errUnsupported, name)
		}
		*p = val
	}
	for _, ch := range []string{c.Channel, c.ReplyChan} {
		if ch != "" && ch != "telegram" {
			return c, fmt.Errorf("%w: channel %q", errUnsupported, ch)
		}
	}
	return c, nil
}

// requests maps a compat command onto the native API.
func (c compatCmd) sendRequest() api.SendRequest {
	return api.SendRequest{To: "telegram:" + c.Account + ":" + c.Target, Text: c.Message}
}

func (c compatCmd) runRequest() (api.RunRequest, error) {
	prompt := c.Message
	if c.MessageFile != "" {
		b, err := os.ReadFile(c.MessageFile)
		if err != nil {
			return api.RunRequest{}, err
		}
		prompt = string(b)
	}
	req := api.RunRequest{Agent: c.Agent, Prompt: prompt, TimeoutSeconds: c.Timeout}
	if c.Deliver && c.ReplyAcct != "" && c.ReplyTo != "" {
		req.To = "telegram:" + c.ReplyAcct + ":" + c.ReplyTo
		// openclaw's "agent:<a>:telegram:direct:<chat>" key is the chat's own
		// conversation; any other key is a separate conversation whose reply
		// is delivered to the chat.
		if c.SessionKey != "" && c.SessionKey != "agent:"+c.Agent+":telegram:direct:"+c.ReplyTo {
			req.Session = c.SessionKey
		}
	} else {
		req.Session = c.SessionKey
	}
	return req, nil
}

// eventPrompt frames a system event for the agent.
const eventPrompt = `System event from a scheduled job:

%s

Handle it as your instructions say. If nothing needs the user's attention, reply with exactly NO_REPLY.`

// eventRequest maps `system event` onto a quiet heartbeat run. The agent
// comes from the session key ("agent:<name>:...") or the configured
// default; the reply goes to the contact whose chat the key names, or to
// the configured notify contact.
func (c compatCmd) eventRequest(cfg *config.Config) (api.RunRequest, error) {
	if strings.TrimSpace(c.Text) == "" {
		return api.RunRequest{}, errors.New("system event: --text is required")
	}
	agentName := cfg.OpenClawCompat.DefaultAgent
	var chat int64
	if parts := strings.Split(c.SessionKey, ":"); len(parts) >= 2 && parts[0] == "agent" && parts[1] != "" {
		agentName = parts[1]
		if len(parts) == 5 && parts[2] == "telegram" && parts[3] == "direct" {
			chat, _ = strconv.ParseInt(parts[4], 10, 64)
		}
	}
	to := cfg.OpenClawCompat.Notify
	if chat != 0 {
		to = ""
		for _, ct := range cfg.Contacts {
			if ct.TelegramChat == chat {
				to = ct.Name
				break
			}
		}
		if to == "" {
			return api.RunRequest{}, fmt.Errorf("system event: no contact has telegram_chat %d", chat)
		}
	}
	if to == "" {
		return api.RunRequest{}, errors.New("system event: set openclaw_compat.notify to the contact that should receive events")
	}
	req := api.RunRequest{
		Agent:     agentName,
		Prompt:    fmt.Sprintf(eventPrompt, c.Text),
		To:        to,
		Quiet:     true,
		Heartbeat: true,
		Async:     !c.ExpectFinal,
	}
	if c.ExpectFinal && c.Timeout > 0 {
		req.TimeoutSeconds = (c.Timeout + 999) / 1000
	}
	return req, nil
}

// cmdOpenClawCompat runs an openclaw-style command against ganclaw, handing
// it to the real openclaw CLI when ganclaw doesn't know the bot or agent.
func cmdOpenClawCompat(ctx context.Context, args []string) error {
	fallback := compatFallback()
	c, err := parseCompat(args)
	if err != nil {
		if fallback != "" {
			return execFallback(fallback, args)
		}
		return &exitError{code: exitUsage, err: err}
	}
	sock, err := socketPath("", defaultConfigPath())
	if err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	client := api.NewClient(sock)

	switch c.Kind {
	case "event":
		cfg, err := config.Load(defaultConfigPath())
		if err != nil {
			return &exitError{code: exitUsage, err: err}
		}
		req, err := c.eventRequest(cfg)
		if err != nil {
			return &exitError{code: exitUsage, err: err}
		}
		resp, err := client.Run(ctx, req)
		if unknownTarget(err) && fallback != "" {
			return execFallback(fallback, args)
		}
		if err != nil {
			return compatFail(c, err)
		}
		if c.JSON {
			printJSON(os.Stdout, map[string]any{"ok": true, "queued": resp.Queued})
		} else {
			fmt.Println("Event delivered to agent", req.Agent+".")
		}
		return nil
	case "send":
		_, err = client.Send(ctx, c.sendRequest())
		if unknownTarget(err) && fallback != "" {
			return execFallback(fallback, args)
		}
		if err != nil {
			return compatFail(c, err)
		}
		if c.JSON {
			printJSON(os.Stdout, map[string]any{"ok": true, "payload": map[string]any{"ok": true}})
		}
		return nil
	default:
		req, err := c.runRequest()
		if err != nil {
			return &exitError{code: exitUsage, err: err}
		}
		resp, err := client.Run(ctx, req)
		if unknownTarget(err) && fallback != "" {
			return execFallback(fallback, args)
		}
		if err != nil {
			return compatFail(c, err)
		}
		if c.JSON {
			printJSON(os.Stdout, map[string]any{
				"status": "ok",
				"result": map[string]any{"payloads": []map[string]string{{"text": resp.Text}}, "provider": resp.Provider},
			})
		} else if !c.Deliver {
			fmt.Println(resp.Text)
		}
		return nil
	}
}

func unknownTarget(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && (ae.Code == api.CodeUnknownBot || ae.Code == api.CodeUnknownAgent)
}

func compatFail(c compatCmd, err error) error {
	if c.JSON {
		var ae *api.Error
		if !errors.As(err, &ae) {
			ae = &api.Error{Code: api.CodeFailed, Message: err.Error()}
		}
		printJSON(os.Stdout, map[string]any{"status": "error", "ok": false, "error": ae})
	}
	return err
}

// compatFallback is the real openclaw CLI, from $GANCLAW_OPENCLAW_BIN or
// openclaw_compat.fallback_bin. It is disabled inside a fallback call so a
// symlinked shim can't loop.
func compatFallback() string {
	if os.Getenv("GANCLAW_COMPAT_FALLBACK") == "1" {
		return ""
	}
	bin := os.Getenv("GANCLAW_OPENCLAW_BIN")
	if bin == "" {
		if cfg, err := config.Load(defaultConfigPath()); err == nil {
			bin = cfg.OpenClawCompat.FallbackBin
		}
	}
	if bin == "" {
		return ""
	}
	self, _ := os.Executable()
	if same(self, bin) {
		return ""
	}
	return bin
}

func same(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func execFallback(bin string, args []string) error {
	env := append(os.Environ(), "GANCLAW_COMPAT_FALLBACK=1")
	return syscall.Exec(bin, append([]string{bin}, args...), env)
}
