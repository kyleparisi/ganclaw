// Package antigravity runs the Antigravity ACP server for each turn.
package antigravity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}
type Config struct {
	Bin, Model, Mode, Sandbox string
	MCPServers                []MCPServer
	Environ                   func() []string
	Exec                      procexec.Exec
}
type Provider struct{ cfg Config }

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "agy"
	}
	if cfg.Mode == "" {
		cfg.Mode = "accept-edits"
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = "workspace-write"
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Exec.Start == nil {
		cfg.Exec = procexec.OS
	}
	return &Provider{cfg: cfg}
}
func (*Provider) Name() string { return "antigravity" }
func (*Provider) Close() error { return nil }

type wire struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string { return fmt.Sprintf("ACP error %d: %s", e.Code, e.Message) }

func turnError(err error) error {
	if err == nil {
		return nil
	}
	var rpc *rpcError
	if !errors.As(err, &rpc) {
		return fmt.Errorf("antigravity: %w: %v", provider.ErrUnavailable, err)
	}
	lower := strings.ToLower(rpc.Message)
	for _, term := range []string{"quota", "rate limit", "resource exhausted", "unauthenticated", "authentication", "connection reset", "unavailable", "timed out", "overloaded"} {
		if strings.Contains(lower, term) {
			return fmt.Errorf("antigravity: %w: %v", provider.ErrUnavailable, err)
		}
	}
	return err
}

type client struct {
	enc     *json.Encoder
	dec     *json.Decoder
	req     provider.Request
	servers []MCPServer
	text    strings.Builder
}

func (c *client) call(id int, method string, params any) (json.RawMessage, error) {
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var msg wire
		if err := c.dec.Decode(&msg); err != nil {
			return nil, err
		}
		if msg.Method == "session/update" {
			var p struct {
				Update struct {
					SessionUpdate string                      `json:"sessionUpdate"`
					Content       struct{ Type, Text string } `json:"content"`
				} `json:"update"`
			}
			if json.Unmarshal(msg.Params, &p) == nil {
				if p.Update.SessionUpdate == "agent_message_chunk" && p.Update.Content.Type == "text" {
					c.text.WriteString(p.Update.Content.Text)
				}
				if p.Update.SessionUpdate == "tool_call" {
					c.text.Reset()
					if c.req.OnProgress != nil {
						c.req.OnProgress("Tool activity observed")
					}
				}
			}
			continue
		}
		if msg.Method != "" && len(msg.ID) > 0 {
			if msg.Method == "session/request_permission" {
				var p struct {
					ToolCall struct {
						Name string `json:"name"`
					} `json:"toolCall"`
					Options []struct {
						OptionID string `json:"optionId"`
						Kind     string `json:"kind"`
					} `json:"options"`
				}
				_ = json.Unmarshal(msg.Params, &p)
				allowed := false
				for _, s := range c.servers {
					if strings.HasPrefix(p.ToolCall.Name, "mcp_"+s.Name+"_") {
						allowed = true
					}
				}
				desired := "reject_once"
				if allowed {
					desired = "allow_once"
				}
				outcome := map[string]any{"outcome": "cancelled"}
				for _, o := range p.Options {
					if o.Kind == desired {
						outcome = map[string]any{"outcome": "selected", "optionId": o.OptionID}
						break
					}
				}
				if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": outcome}}); err != nil {
					return nil, err
				}
			} else {
				if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "method not supported"}}); err != nil {
					return nil, err
				}
			}
			continue
		}
		if string(msg.ID) == fmt.Sprint(id) {
			if msg.Error != nil {
				return nil, &rpcError{Code: msg.Error.Code, Message: msg.Error.Message}
			}
			return msg.Result, nil
		}
	}
}
func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	cwd, err := filepath.Abs(req.Cwd)
	if err != nil {
		return provider.Result{}, err
	}
	env := p.cfg.Environ()
	bin, args := p.cfg.Bin, []string{"acp"}
	sandbox := req.Settings.CodexSandbox
	if sandbox == "" {
		sandbox = p.cfg.Sandbox
	}
	if sandbox == "read-only" || sandbox == "workspace-write" || p.cfg.Mode == "auto" || req.Settings.AntigravityMode == "auto" {
		args = append(args, "--sandbox")
	}
	proc, err := p.cfg.Exec.Start(ctx, procexec.Cmd{Bin: bin, Args: args, Env: env, Dir: cwd, Stderr: io.Discard})
	if err != nil {
		return provider.Result{}, fmt.Errorf("start antigravity: %w: %v", provider.ErrUnavailable, err)
	}
	defer func() {
		_ = proc.Stdin.Close()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, proc.Stdout); _ = proc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if proc.Kill != nil {
				_ = proc.Kill()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}
	}()
	// Give each configured server an unpredictable session-specific name so a
	// workspace or global server cannot impersonate one when requesting approval.
	sessionServers := make([]MCPServer, len(p.cfg.MCPServers))
	for i, server := range p.cfg.MCPServers {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return provider.Result{}, err
		}
		sessionServers[i] = server
		sessionServers[i].Name = "gc_" + hex.EncodeToString(nonce[:])
	}
	c := &client{enc: json.NewEncoder(proc.Stdin), dec: json.NewDecoder(proc.Stdout), req: req, servers: sessionServers}
	fail := func(err error) (provider.Result, error) {
		if ctx.Err() != nil {
			return provider.Result{}, ctx.Err()
		}
		return provider.Result{}, fmt.Errorf("antigravity: %w: %v", provider.ErrUnavailable, err)
	}
	if _, err = c.call(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}); err != nil {
		return fail(err)
	}
	var servers []any
	for _, s := range sessionServers {
		env := make([]any, 0, len(s.Env))
		for k, v := range s.Env {
			env = append(env, map[string]string{"name": k, "value": v})
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		servers = append(servers, map[string]any{"name": s.Name, "command": s.Command, "args": args, "env": env})
	}
	if servers == nil {
		servers = []any{}
	}
	params := map[string]any{"cwd": cwd, "mcpServers": servers}
	instructions := req.Instructions
	if len(sessionServers) > 0 {
		instructions += "\n\nMCP server names for this session:\n"
		for i, s := range sessionServers {
			instructions += "- " + p.cfg.MCPServers[i].Name + ": " + s.Name + "\n"
		}
	}
	if instructions != "" {
		params["_meta"] = map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": instructions}}}
	}
	method := "session/new"
	if req.Session != "" {
		method = "session/resume"
		params["sessionId"] = req.Session
	}
	raw, err := c.call(2, method, params)
	if err != nil && req.Session != "" {
		var rpc *rpcError
		if errors.As(err, &rpc) && strings.Contains(strings.ToLower(rpc.Message), "failed to read session") {
			delete(params, "sessionId")
			raw, err = c.call(6, "session/new", params)
			if err == nil {
				req.Session = ""
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err = json.Unmarshal(raw, &session); err != nil {
		return fail(err)
	}
	if req.Session != "" {
		session.SessionID = req.Session
	}
	if session.SessionID == "" {
		return fail(errors.New("missing session ID"))
	}
	res := provider.Result{Session: session.SessionID}
	mode := p.cfg.Mode
	if req.Settings.AntigravityMode != "" {
		mode = req.Settings.AntigravityMode
	}
	if sandbox == "read-only" || req.Settings.PermissionMode == "plan" {
		mode = "plan"
	}
	if mode != "" {
		if _, err = c.call(3, "session/set_config_option", map[string]any{"sessionId": session.SessionID, "configId": "mode", "value": mode}); err != nil {
			return res, turnError(err)
		}
	}
	model := p.cfg.Model
	if req.Settings.AntigravityModel != "" {
		model = req.Settings.AntigravityModel
	}
	if model != "" {
		if _, err = c.call(4, "session/set_config_option", map[string]any{"sessionId": session.SessionID, "configId": "model", "value": model}); err != nil {
			return res, turnError(err)
		}
	}
	c.text.Reset()
	raw, err = c.call(5, "session/prompt", map[string]any{"sessionId": session.SessionID, "prompt": []any{map[string]any{"type": "text", "text": req.Prompt}}})
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, turnError(err)
	}
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return res, err
	}
	if result.StopReason != "end_turn" {
		return res, fmt.Errorf("antigravity stopped: %s", result.StopReason)
	}
	res.Text = strings.TrimSpace(c.text.String())
	if res.Text == "" {
		return res, errors.New("antigravity returned no response text")
	}
	if req.OnDelta != nil {
		req.OnDelta(res.Text)
	}
	return res, nil
}
