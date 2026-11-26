// Package claude runs the Claude Code CLI (`claude -p`) once per turn and
// parses its stream-json output. The CLI owns the login (subscription, via
// `claude login`) and session history; sessions resume by ID.
package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

type Config struct {
	// Bin is the claude executable. Defaults to "claude" on PATH.
	Bin string
	// ConfigDir sets CLAUDE_CONFIG_DIR, isolating login, settings, plugins
	// and session history from the invoking user's own Claude Code setup.
	// Empty uses the default (~/.claude).
	ConfigDir string
	// Model overrides the CLI's default model. Optional.
	Model string
	// PermissionMode is passed to --permission-mode. Defaults to
	// "acceptEdits": file edits are allowed, anything that would prompt is
	// denied (there is no one to ask in print mode).
	PermissionMode string
	// MCPConfig is a JSON file of MCP servers (--mcp-config). Other MCP
	// configuration is ignored (--strict-mcp-config). Optional.
	MCPConfig string
	// AllowedTools may run without a permission prompt, e.g. "WebSearch"
	// or "mcp__browser" (every tool from that MCP server).
	AllowedTools []string
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string
	// Environ returns the base environment. Defaults to os.Environ.
	Environ func() []string
	// Exec starts the CLI. Zero value uses real processes.
	Exec procexec.Exec
	// Logger receives provider events and CLI stderr (at debug).
	Logger *slog.Logger
}

type Provider struct {
	cfg Config
	log *slog.Logger

	mu        sync.Mutex
	rateLimit *rateLimitInfo // latest report from any turn
	now       func() time.Time
}

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "claude"
	}
	if cfg.PermissionMode == "" {
		cfg.PermissionMode = "acceptEdits"
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Exec.Start == nil {
		cfg.Exec = procexec.OS
	}
	return &Provider{cfg: cfg, log: logx.OrDiscard(cfg.Logger).With("provider", "claude"), now: time.Now}
}

func (p *Provider) Name() string { return "claude" }
func (p *Provider) Close() error { return nil }

// Run executes one turn in a fresh CLI process.
func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	start := time.Now()
	log := p.log
	if req.Session != "" {
		log = log.With("session", req.Session)
	}

	var stderr bytes.Buffer
	proc, err := p.cfg.Exec.Start(ctx, procexec.Cmd{
		Bin: p.cfg.Bin,
		// Prompt goes on stdin so it can't be mistaken for a flag and
		// isn't limited by argv size.
		Args:   p.args(req),
		Env:    p.env(),
		Dir:    req.Cwd,
		Stdin:  strings.NewReader(req.Prompt),
		Stderr: io.MultiWriter(logx.LineWriter(log, slog.LevelDebug, "claude stderr"), &tail{buf: &stderr, max: 4096}),
	})
	if err != nil {
		return provider.Result{}, &Error{Message: "start claude: " + err.Error(), Unavailable: true}
	}
	log.Debug("turn started", "prompt_len", len(req.Prompt))

	st, parseErr := parseStream(proc.Stdout, req.OnDelta, req.OnProgress)
	waitErr := proc.Wait()
	if st.rateLimit != nil {
		p.mu.Lock()
		p.rateLimit = st.rateLimit
		p.mu.Unlock()
	}

	res := provider.Result{Session: st.session}
	if ctx.Err() != nil {
		log.Info("turn cancelled")
		return res, ctx.Err()
	}
	if st.result == nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && parseErr != nil {
			msg = parseErr.Error()
		}
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		err := &Error{Message: "claude exited without a result: " + msg, Unavailable: true}
		log.Warn("turn failed", "err", err, "duration", time.Since(start))
		return res, err
	}
	if st.result.SessionID != "" {
		res.Session = st.result.SessionID
	}
	log = log.With("session", res.Session)
	if err := st.err(); err != nil {
		log.Warn("turn failed", "err", err, "duration", time.Since(start))
		return res, err
	}
	res.Text = strings.TrimSpace(st.result.Result)
	log.Info("turn completed", "reply_len", len(res.Text), "duration", time.Since(start))
	return res, nil
}

func (p *Provider) args(req provider.Request) []string {
	perm := firstNonEmpty(req.Settings.PermissionMode, p.cfg.PermissionMode)
	readOnly := req.Settings.NoMCP || perm == "plan"
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", perm,
	}
	if m := firstNonEmpty(req.Settings.ClaudeModel, p.cfg.Model); m != "" {
		args = append(args, "--model", m)
	}
	if req.Settings.NoMCP {
		args = append(args, "--strict-mcp-config")
	} else if p.cfg.MCPConfig != "" {
		args = append(args, "--mcp-config", p.cfg.MCPConfig, "--strict-mcp-config")
	}
	var allowed []string
	for _, t := range p.cfg.AllowedTools {
		if req.Settings.NoMCP && strings.HasPrefix(t, "mcp__") {
			continue
		}
		if readOnly && !isReadOnlyClaudeTool(t) {
			continue
		}
		allowed = append(allowed, t)
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if readOnly {
		args = append(args, "--disallowedTools", "Edit,Write,NotebookEdit")
	}
	if req.Session != "" {
		args = append(args, "--resume", req.Session)
	}
	// The system prompt isn't stored with a session, so it's passed every
	// time; changes to an agent's files reach existing conversations.
	if req.Instructions != "" {
		args = append(args, "--append-system-prompt", req.Instructions)
	}
	extra := p.cfg.ExtraArgs
	if readOnly {
		extra = filterReadOnlyExtraArgs(extra)
	}
	return append(args, extra...)
}

func isReadOnlyClaudeTool(tool string) bool {
	switch tool {
	case "Read", "Grep", "Glob", "WebSearch", "WebFetch":
		return true
	}
	return false
}

// filterReadOnlyExtraArgs removes global ExtraArgs that could re-enable MCP
// servers or loosen permissions during a read-only / NoMCP turn.
func filterReadOnlyExtraArgs(extra []string) []string {
	var out []string
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		name, hasEq := arg, false
		if k, _, ok := strings.Cut(arg, "="); ok {
			name, hasEq = k, true
		}
		switch name {
		case "--mcp-config", "--permission-mode", "--allowedTools", "--allowed-tools", "--disallowedTools", "--disallowed-tools":
			if !hasEq && i+1 < len(extra) {
				i++
			}
			continue
		case "--dangerously-skip-permissions":
			continue
		}
		out = append(out, arg)
	}
	return out
}

// env returns the base environment with API-key credentials removed, so the
// CLI always uses its own subscription login and never bills an API key that
// happens to be set in the gateway's environment.
func (p *Provider) env() []string {
	var env []string
	for _, kv := range p.cfg.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR", "CLAUDECODE":
			continue
		}
		env = append(env, kv)
	}
	if p.cfg.ConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+p.cfg.ConfigDir)
	}
	return env
}

// Error is a failed claude turn.
type Error struct {
	Subtype     string // result subtype, e.g. "error_during_execution"
	Status      int    // upstream API HTTP status, if any
	Message     string
	Unavailable bool
	Retry       time.Time // when a rejected rate limit resets
}

// RetryAt implements provider.RetryAter.
func (e *Error) RetryAt() time.Time { return e.Retry }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("claude turn failed")
	if e.Subtype != "" {
		fmt.Fprintf(&b, " (%s)", e.Subtype)
	}
	if e.Status != 0 {
		fmt.Fprintf(&b, " [HTTP %d]", e.Status)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

func (e *Error) Is(target error) bool { return e.Unavailable && target == provider.ErrUnavailable }

// tail keeps the last max bytes written, for error messages.
type tail struct {
	buf *bytes.Buffer
	max int
}

func (t *tail) Write(b []byte) (int, error) {
	t.buf.Write(b)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(b), nil
}

var errNoResult = errors.New("stream ended without a result event")

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
