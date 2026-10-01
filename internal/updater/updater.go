// Package updater replaces the codex and claude CLIs with their latest
// releases, verifying each download against its published SHA-256 and
// keeping the previous binary for rollback.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Release is a downloadable CLI build.
type Release struct {
	Version string
	URL     string
	SHA256  string // of the downloaded file
	// Member, if set, is the file to extract from a .tar.gz download.
	Member string
}

// HTTPClient performs requests. Tests replace Do with an inline fake.
type HTTPClient struct {
	Do func(req *http.Request) (*http.Response, error)
}

// Runner runs a command and returns its combined output. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// RunCommand runs a real command.
func RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Updater fetches releases and installs them.
type Updater struct {
	HTTP HTTPClient
	Run  Runner
	// ClaudeBase is where Claude Code releases are published.
	ClaudeBase string
	// CodexAPI is the GitHub API URL of the latest codex release.
	CodexAPI string
}

const (
	DefaultClaudeBase = "https://downloads.claude.ai/claude-code-releases"
	DefaultCodexAPI   = "https://api.github.com/repos/openai/codex/releases/latest"
)

var versionRe = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?`)

// InstalledVersion runs "bin --version" and returns the version in its
// output, or "" if it can't tell.
func (u *Updater) InstalledVersion(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := u.Run(ctx, bin, "--version")
	if err != nil {
		return ""
	}
	return versionRe.FindString(out)
}

// LatestClaude returns the current Claude Code release on a channel
// ("stable" or "latest") for a platform such as "linux-x64".
func (u *Updater) LatestClaude(ctx context.Context, channel, platform string) (Release, error) {
	base := strings.TrimRight(u.ClaudeBase, "/")
	raw, err := u.get(ctx, base+"/"+channel, 1<<10)
	if err != nil {
		return Release{}, fmt.Errorf("claude %s version: %w", channel, err)
	}
	version := strings.TrimSpace(string(raw))
	if versionRe.FindString(version) != version {
		return Release{}, fmt.Errorf("claude %s version: unexpected response %q", channel, truncate(version, 60))
	}
	raw, err = u.get(ctx, base+"/"+version+"/manifest.json", 1<<20)
	if err != nil {
		return Release{}, fmt.Errorf("claude %s manifest: %w", version, err)
	}
	var m struct {
		Platforms map[string]struct {
			Checksum string `json:"checksum"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Release{}, fmt.Errorf("claude %s manifest: %w", version, err)
	}
	sum := m.Platforms[platform].Checksum
	if !isSHA256(sum) {
		return Release{}, fmt.Errorf("claude %s manifest: no checksum for %s", version, platform)
	}
	return Release{Version: version, URL: base + "/" + version + "/" + platform + "/claude", SHA256: sum}, nil
}

// LatestCodex returns the latest codex release for a target such as
// "x86_64-unknown-linux-musl".
func (u *Updater) LatestCodex(ctx context.Context, target string) (Release, error) {
	raw, err := u.get(ctx, u.CodexAPI, 4<<20)
	if err != nil {
		return Release{}, fmt.Errorf("codex latest release: %w", err)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &rel); err != nil {
		return Release{}, fmt.Errorf("codex latest release: %w", err)
	}
	version := versionRe.FindString(rel.Tag)
	if version == "" {
		return Release{}, fmt.Errorf("codex latest release: no version in tag %q", rel.Tag)
	}
	name := "codex-" + target + ".tar.gz"
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		sum := strings.TrimPrefix(a.Digest, "sha256:")
		if !isSHA256(sum) {
			return Release{}, fmt.Errorf("codex %s: no sha256 digest for %s", version, name)
		}
		return Release{Version: version, URL: a.URL, SHA256: sum, Member: "codex-" + target}, nil
	}
	return Release{}, fmt.Errorf("codex %s: no asset %s", version, name)
}

// Install downloads rel, verifies it, checks that it runs and reports
// rel.Version, then puts it at dest. The binary it replaces is kept as
// dest+".prev". Processes already running the old binary are unaffected.
func (u *Updater) Install(ctx context.Context, rel Release, dest string) error {
	dir := filepath.Dir(dest)
	dl, err := os.CreateTemp(dir, ".ganclaw-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(dl.Name())
	defer dl.Close()

	if err := u.download(ctx, rel, dl); err != nil {
		return err
	}

	staged := dest + ".new"
	defer os.Remove(staged)
	if rel.Member != "" {
		if _, err := dl.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := extract(dl, rel.Member, staged); err != nil {
			return err
		}
	} else {
		if err := dl.Close(); err != nil {
			return err
		}
		if err := os.Rename(dl.Name(), staged); err != nil {
			return err
		}
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	if got := u.InstalledVersion(ctx, staged); got != rel.Version {
		return fmt.Errorf("downloaded %s reports version %q, want %s", filepath.Base(dest), got, rel.Version)
	}

	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, dest+".prev"); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, dest); err != nil {
		// Put the old binary back rather than leave nothing in place.
		_ = os.Rename(dest+".prev", dest)
		return err
	}
	return nil
}

// Rollback restores the binary Install replaced.
func Rollback(dest string) error {
	if _, err := os.Stat(dest + ".prev"); err != nil {
		return fmt.Errorf("no previous %s to restore: %w", filepath.Base(dest), err)
	}
	return os.Rename(dest+".prev", dest)
}

// download streams rel into f, checking its SHA-256.
func (u *Updater) download(ctx context.Context, rel Release, f *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", rel.URL, resp.StatusCode)
	}
	h := sha256.New()
	// Release binaries are a few hundred MB; refuse anything absurd.
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 2<<30)); err != nil {
		return fmt.Errorf("download %s: %w", rel.URL, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.SHA256 {
		return fmt.Errorf("download %s: sha256 %s does not match published %s", rel.URL, got, rel.SHA256)
	}
	return nil
}

// extract writes one regular file from a .tar.gz to dest.
func extract(r io.Reader, member, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in archive", member)
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != member {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, 2<<30)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
