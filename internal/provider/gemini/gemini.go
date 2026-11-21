package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

// Config controls the Gemini CLI subprocess.
type Config struct {
	Home         string
	Bin          string
	Model        string
	ApprovalMode string
	ServerNames  []string
	Environ      func() []string
	Exec         procexec.Exec
}

type Provider struct{ cfg Config }

func New(cfg Config) *Provider {
	if cfg.Bin == "" {
		cfg.Bin = "gemini"
	}
	if cfg.ApprovalMode == "" {
		cfg.ApprovalMode = "default"
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Exec.Start == nil {
		cfg.Exec = procexec.OS
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return "gemini" }
func (p *Provider) Close() error { return nil }

func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	approval := req.Settings.GeminiApprovalMode
	if approval == "" {
		approval = p.cfg.ApprovalMode
	}
	args := []string{"--output-format", "stream-json", "--approval-mode", approval}
	model := req.Settings.GeminiModel
	if model == "" {
		model = p.cfg.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(p.cfg.ServerNames) > 0 {
		args = append(args, "--allowed-mcp-server-names", strings.Join(p.cfg.ServerNames, ","))
	}
	if req.Session != "" {
		args = append(args, "--resume", req.Session)
	}
	prompt := req.Prompt
	if req.Instructions != "" {
		prompt = "Agent instructions:\n" + req.Instructions + "\n\nUser request:\n" + prompt
	}
	env := procexec.FilterEnv(p.cfg.Environ(), func(k string) bool {
		return k == "GEMINI_CLI_HOME" && p.cfg.Home != ""
	})
	if p.cfg.Home != "" {
		env = append(env, "GEMINI_CLI_HOME="+p.cfg.Home)
	}

	var stderr bytes.Buffer
	proc, err := p.cfg.Exec.Start(ctx, procexec.Cmd{Bin: p.cfg.Bin, Args: args, Env: env, Dir: req.Cwd, Stdin: strings.NewReader(prompt), Stderr: &limitedWriter{buf: &stderr, max: 4096}})
	if err != nil {
		return provider.Result{}, fmt.Errorf("start gemini: %w: %v", provider.ErrUnavailable, err)
	}
	st, parseErr := parseStream(proc.Stdout, req.OnDelta, req.OnProgress, req.OnReset)
	if parseErr != nil {
		_, _ = io.Copy(io.Discard, proc.Stdout)
	}
	waitErr := proc.Wait()
	res := provider.Result{Session: st.session, Text: strings.TrimSpace(st.text.String())}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if parseErr != nil {
		diagnostic := strings.ToLower(stderr.String())
		if req.Session != "" && st.session == "" && st.text.Len() == 0 && !st.toolUsed && strings.Contains(diagnostic, "error resuming session: invalid session identifier") {
			req.Session = ""
			return p.Run(ctx, req)
		}
		reason := "CLI exited without a valid result"
		switch {
		case strings.Contains(diagnostic, "auth"), strings.Contains(diagnostic, "api key"):
			reason = "authentication required or failed"
		case strings.Contains(diagnostic, "quota"), strings.Contains(diagnostic, "rate limit"):
			reason = "quota or rate limit reached"
		}
		return res, fmt.Errorf("gemini: %w: %s", provider.ErrUnavailable, reason)
	}
	if st.failure != "" {
		lower := strings.ToLower(st.failure)
		if strings.Contains(lower, "rate limit") || strings.Contains(lower, "quota") || strings.Contains(lower, "resource exhausted") || strings.Contains(lower, "resource_exhausted") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "authentication failed") || strings.Contains(lower, "service unavailable") || strings.Contains(lower, "overloaded") || strings.Contains(lower, "authentication") || strings.Contains(lower, "auth method") || strings.Contains(lower, "api key") || strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "fetch failed") || strings.Contains(lower, "etimedout") || strings.Contains(lower, "econnreset") {
			return res, fmt.Errorf("gemini: %w: %s", provider.ErrUnavailable, st.failure)
		}
		return res, errors.New("gemini: " + st.failure)
	}
	if st.status != "success" {
		return res, fmt.Errorf("gemini returned status %q: %w", st.status, provider.ErrUnavailable)
	}
	_ = waitErr // A successful result is authoritative even if cleanup fails.
	return res, nil
}

// limitedWriter retains only the end of stderr for classifying startup errors.
// Raw stderr is not exposed because CLI diagnostics can contain credentials.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	n := len(b)
	if len(b) > w.max {
		b = b[len(b)-w.max:]
	}
	_, _ = w.buf.Write(b)
	if over := w.buf.Len() - w.max; over > 0 {
		w.buf.Next(over)
	}
	return n, nil
}
