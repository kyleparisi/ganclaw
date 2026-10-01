package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/updater"
)

// cmdUpdateCLIs replaces the codex and claude binaries named in the config
// with their latest releases. Run it as root, by hand. Claude is run per
// turn, so a new claude is used from the next turn; codex runs as a
// long-lived app-server, so the gateway is restarted once no turn is
// running, and both binaries are rolled back if it doesn't come back.
func cmdUpdateCLIs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update-clis", flag.ContinueOnError)
	path := fs.String("config", defaultConfigPath(), "config file")
	channel := fs.String("channel", "stable", "claude release channel: stable or latest")
	check := fs.Bool("check", false, "only show installed and available versions")
	restart := fs.Bool("restart", true, "restart ganclaw when idle if codex was updated")
	wait := fs.Duration("wait", 30*time.Minute, "how long to wait for running turns to finish before restarting")
	unit := fs.String("unit", "ganclaw", "systemd unit to restart")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	if *channel != "stable" && *channel != "latest" {
		return &exitError{code: exitUsage, err: fmt.Errorf("update-clis: -channel must be stable or latest")}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return &exitError{code: exitUsage, err: err}
	}
	claudePlatform, codexTarget, err := platforms(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !*check && os.Geteuid() != 0 {
		return &exitError{code: exitUsage, err: errors.New("update-clis: run as root (the binaries are root-owned so agents can't replace them)")}
	}

	u := &updater.Updater{
		HTTP:       updater.HTTPClient{Do: (&http.Client{Timeout: 10 * time.Minute}).Do},
		Run:        updater.RunCommand,
		ClaudeBase: updater.DefaultClaudeBase,
		CodexAPI:   updater.DefaultCodexAPI,
	}
	type cli struct {
		name, bin string
		latest    func() (updater.Release, error)
	}
	clis := []cli{
		{"codex", cfg.Codex.Bin, func() (updater.Release, error) { return u.LatestCodex(ctx, codexTarget) }},
		{"claude", cfg.Claude.Bin, func() (updater.Release, error) { return u.LatestClaude(ctx, *channel, claudePlatform) }},
	}

	var updated []string // binaries replaced, for rollback
	codexUpdated := false
	var failed error
	for _, c := range clis {
		bin, err := resolveBin(c.bin, c.name)
		if err != nil {
			fmt.Printf("%-6s  skipped: %v\n", c.name, err)
			continue
		}
		current := u.InstalledVersion(ctx, bin)
		rel, err := c.latest()
		if err != nil {
			fmt.Printf("%-6s  %s  (couldn't check for updates: %v)\n", c.name, orUnknown(current), err)
			failed = errors.Join(failed, err)
			continue
		}
		if current == rel.Version {
			fmt.Printf("%-6s  %s  up to date\n", c.name, current)
			continue
		}
		if *check {
			fmt.Printf("%-6s  %s → %s available\n", c.name, orUnknown(current), rel.Version)
			continue
		}
		fmt.Printf("%-6s  %s → %s  downloading…\n", c.name, orUnknown(current), rel.Version)
		if err := u.Install(ctx, rel, bin); err != nil {
			fmt.Printf("%-6s  update failed, kept %s: %v\n", c.name, orUnknown(current), err)
			failed = errors.Join(failed, err)
			continue
		}
		fmt.Printf("%-6s  installed %s (previous kept as %s.prev)\n", c.name, rel.Version, filepath.Base(bin))
		updated = append(updated, bin)
		codexUpdated = codexUpdated || c.name == "codex"
	}

	if codexUpdated && *restart {
		r := restarter{
			Health:  api.NewClient(cfg.APISocket).Health,
			Restart: func(ctx context.Context) error { return exec.CommandContext(ctx, "systemctl", "restart", *unit).Run() },
			Sleep:   time.Sleep,
			Log:     func(s string) { fmt.Println(s) },
		}
		if err := r.restartWhenIdle(ctx, *wait); err != nil {
			fmt.Println("ganclaw didn't come back after the restart; restoring the previous binaries")
			for _, bin := range updated {
				if rerr := updater.Rollback(bin); rerr != nil {
					err = errors.Join(err, rerr)
				}
			}
			if rerr := r.Restart(ctx); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return errors.Join(failed, err)
		}
	} else if codexUpdated {
		fmt.Println("restart ganclaw to use the new codex")
	}
	return failed
}

// restarter restarts the gateway at a quiet moment and checks it returns.
type restarter struct {
	Health  func(ctx context.Context) (api.HealthResponse, error)
	Restart func(ctx context.Context) error
	Sleep   func(time.Duration)
	Log     func(string)
}

const pollEvery = 5 * time.Second

// restartWhenIdle waits up to wait for running turns to finish, restarts,
// and waits for the gateway to answer again. A gateway that isn't running
// is left alone: it uses the new binaries when it starts.
func (r restarter) restartWhenIdle(ctx context.Context, wait time.Duration) error {
	h, err := r.Health(ctx)
	if err != nil {
		r.Log("ganclaw isn't running; it will use the new binaries when it starts")
		return nil
	}
	for waited := time.Duration(0); h.ActiveTurns > 0; waited += pollEvery {
		if waited >= wait {
			r.Log(fmt.Sprintf("%d turn(s) still running after %s; restarting anyway", h.ActiveTurns, wait))
			break
		}
		if waited == 0 {
			r.Log(fmt.Sprintf("waiting for %d running turn(s) to finish…", h.ActiveTurns))
		}
		r.Sleep(pollEvery)
		if h, err = r.Health(ctx); err != nil {
			break // stopped meanwhile; the restart below starts it
		}
	}
	r.Log("restarting ganclaw")
	if err := r.Restart(ctx); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	for i := 0; i < 18; i++ {
		r.Sleep(pollEvery)
		if h, err = r.Health(ctx); err == nil {
			r.Log("ganclaw is back (" + h.Version + ")")
			return nil
		}
	}
	return fmt.Errorf("ganclaw not answering after restart: %w", err)
}

// platforms maps Go's OS/arch to the names the releases use.
func platforms(goos, goarch string) (claude, codex string, err error) {
	if goos != "linux" {
		return "", "", fmt.Errorf("update-clis: only Linux is supported, not %s", goos)
	}
	switch goarch {
	case "amd64":
		return "linux-x64", "x86_64-unknown-linux-musl", nil
	case "arm64":
		return "linux-arm64", "aarch64-unknown-linux-musl", nil
	}
	return "", "", fmt.Errorf("update-clis: unsupported architecture %s", goarch)
}

// resolveBin finds the configured binary, which must be a file we can
// replace (not, say, a symlink into a package manager's tree).
func resolveBin(bin, name string) (string, error) {
	if bin == "" {
		bin = name
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	if p, err = filepath.Abs(p); err != nil {
		return "", err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file; set [%s] bin to a standalone binary", p, name)
	}
	return p, nil
}

func orUnknown(v string) string {
	if v == "" {
		return "(unknown version)"
	}
	return v
}
