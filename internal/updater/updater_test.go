package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func respond(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}
}

// routes serves fixed bodies by URL; anything else is a 404.
func routes(t *testing.T, bodies map[string][]byte) HTTPClient {
	return HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
		if b, ok := bodies[req.URL.String()]; ok {
			return respond(http.StatusOK, b), nil
		}
		t.Logf("unexpected request %s", req.URL)
		return respond(http.StatusNotFound, nil), nil
	}}
}

// versionRunner fakes "--version" by reading the version from the binary,
// which tests write as "VERSION <x.y.z>".
func versionRunner(ctx context.Context, name string, args ...string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(string(b), "VERSION ") + " (fake)\n", nil
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestLatestClaude(t *testing.T) {
	ctx := context.Background()
	const base = "https://dl.test/releases"

	t.Run("SuccessFlow", func(t *testing.T) {
		subject := &Updater{ClaudeBase: base, HTTP: routes(t, map[string][]byte{
			base + "/stable":                []byte("2.1.290\n"),
			base + "/2.1.290/manifest.json": []byte(`{"platforms":{"linux-x64":{"binary":"claude","checksum":"` + strings.Repeat("ab", 32) + `"}}}`),
		})}

		rel, err := subject.LatestClaude(ctx, "stable", "linux-x64")

		require.NoError(t, err)
		assert.Equal(t, Release{Version: "2.1.290", URL: base + "/2.1.290/linux-x64/claude", SHA256: strings.Repeat("ab", 32)}, rel)
	})

	t.Run("A non-version channel response is refused", func(t *testing.T) {
		subject := &Updater{ClaudeBase: base, HTTP: routes(t, map[string][]byte{base + "/stable": []byte("<html>blocked</html>")})}

		_, err := subject.LatestClaude(ctx, "stable", "linux-x64")

		assert.ErrorContains(t, err, "unexpected response")
	})

	t.Run("A missing checksum is an error", func(t *testing.T) {
		subject := &Updater{ClaudeBase: base, HTTP: routes(t, map[string][]byte{
			base + "/stable":                []byte("2.1.290"),
			base + "/2.1.290/manifest.json": []byte(`{"platforms":{"darwin-arm64":{"checksum":"` + strings.Repeat("ab", 32) + `"}}}`),
		})}

		_, err := subject.LatestClaude(ctx, "stable", "linux-x64")

		assert.ErrorContains(t, err, "no checksum for linux-x64")
	})
}

func TestLatestCodex(t *testing.T) {
	ctx := context.Background()
	const api = "https://api.test/latest"

	t.Run("SuccessFlow", func(t *testing.T) {
		subject := &Updater{CodexAPI: api, HTTP: routes(t, map[string][]byte{api: []byte(`{"tag_name":"rust-v0.160.0","assets":[
			{"name":"codex-aarch64-apple-darwin.tar.gz","browser_download_url":"https://gh.test/mac","digest":"sha256:` + strings.Repeat("cd", 32) + `"},
			{"name":"codex-x86_64-unknown-linux-musl.tar.gz","browser_download_url":"https://gh.test/linux","digest":"sha256:` + strings.Repeat("ef", 32) + `"}]}`)})}

		rel, err := subject.LatestCodex(ctx, "x86_64-unknown-linux-musl")

		require.NoError(t, err)
		assert.Equal(t, Release{Version: "0.160.0", URL: "https://gh.test/linux", SHA256: strings.Repeat("ef", 32), Member: "codex-x86_64-unknown-linux-musl"}, rel)
	})

	t.Run("An asset without a digest is refused", func(t *testing.T) {
		subject := &Updater{CodexAPI: api, HTTP: routes(t, map[string][]byte{api: []byte(`{"tag_name":"rust-v0.160.0","assets":[
			{"name":"codex-x86_64-unknown-linux-musl.tar.gz","browser_download_url":"https://gh.test/linux"}]}`)})}

		_, err := subject.LatestCodex(ctx, "x86_64-unknown-linux-musl")

		assert.ErrorContains(t, err, "no sha256 digest")
	})
}

func TestInstall(t *testing.T) {
	ctx := context.Background()

	t.Run("SuccessFlow", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "claude")
		require.NoError(t, os.WriteFile(dest, []byte("VERSION 1.0.0"), 0o755))
		bin := []byte("VERSION 1.1.0")
		subject := &Updater{Run: versionRunner, HTTP: routes(t, map[string][]byte{"https://dl.test/claude": bin})}

		err := subject.Install(ctx, Release{Version: "1.1.0", URL: "https://dl.test/claude", SHA256: sum(bin)}, dest)

		require.NoError(t, err)
		assert.Equal(t, "1.1.0", subject.InstalledVersion(ctx, dest))
		assert.Equal(t, "1.0.0", subject.InstalledVersion(ctx, dest+".prev"))
		fi, _ := os.Stat(dest)
		assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
		entries, _ := os.ReadDir(dir)
		assert.Len(t, entries, 2, "no temporary files are left behind")
	})

	t.Run("Archives are unpacked", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "codex")
		archive := tarGz(t, "codex-x86_64-unknown-linux-musl", []byte("VERSION 0.160.0"))
		subject := &Updater{Run: versionRunner, HTTP: routes(t, map[string][]byte{"https://gh.test/linux": archive})}

		err := subject.Install(ctx, Release{Version: "0.160.0", URL: "https://gh.test/linux", SHA256: sum(archive), Member: "codex-x86_64-unknown-linux-musl"}, dest)

		require.NoError(t, err)
		assert.Equal(t, "0.160.0", subject.InstalledVersion(ctx, dest))
	})

	t.Run("A checksum mismatch leaves the installed binary alone", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "claude")
		require.NoError(t, os.WriteFile(dest, []byte("VERSION 1.0.0"), 0o755))
		subject := &Updater{Run: versionRunner, HTTP: routes(t, map[string][]byte{"https://dl.test/claude": []byte("VERSION 6.6.6")})}

		err := subject.Install(ctx, Release{Version: "1.1.0", URL: "https://dl.test/claude", SHA256: strings.Repeat("00", 32)}, dest)

		assert.ErrorContains(t, err, "does not match published")
		assert.Equal(t, "1.0.0", subject.InstalledVersion(ctx, dest))
		entries, _ := os.ReadDir(dir)
		assert.Len(t, entries, 1)
	})

	t.Run("A binary that doesn't report the expected version is not installed", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "claude")
		require.NoError(t, os.WriteFile(dest, []byte("VERSION 1.0.0"), 0o755))
		bin := []byte("VERSION 0.9.0")
		subject := &Updater{Run: versionRunner, HTTP: routes(t, map[string][]byte{"https://dl.test/claude": bin})}

		err := subject.Install(ctx, Release{Version: "1.1.0", URL: "https://dl.test/claude", SHA256: sum(bin)}, dest)

		assert.ErrorContains(t, err, `reports version "0.9.0", want 1.1.0`)
		assert.Equal(t, "1.0.0", subject.InstalledVersion(ctx, dest))
	})

	t.Run("Download failures are errors", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "claude")
		subject := &Updater{Run: versionRunner, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		}}}

		err := subject.Install(ctx, Release{Version: "1.1.0", URL: "https://dl.test/claude", SHA256: strings.Repeat("00", 32)}, dest)

		assert.ErrorContains(t, err, "network down")
	})
}

func TestRollback(t *testing.T) {
	subject := Rollback

	t.Run("Restores the previous binary", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "codex")
		require.NoError(t, os.WriteFile(dest, []byte("new"), 0o755))
		require.NoError(t, os.WriteFile(dest+".prev", []byte("old"), 0o755))

		require.NoError(t, subject(dest))

		b, _ := os.ReadFile(dest)
		assert.Equal(t, "old", string(b))
		assert.NoFileExists(t, dest+".prev")
	})

	t.Run("Nothing to restore is an error", func(t *testing.T) {
		assert.ErrorContains(t, subject(filepath.Join(t.TempDir(), "codex")), "no previous codex")
	})
}
