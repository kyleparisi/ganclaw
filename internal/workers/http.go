package workers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type WorkerCredential struct {
	TokenFile string   `json:"token_file"`
	Profiles  []string `json:"profiles"`
}
type BrokerConfig struct {
	Listen              string                      `json:"listen"`
	Certificate         string                      `json:"certificate"`
	Key                 string                      `json:"key"`
	StateDir            string                      `json:"state_dir"`
	ControllerTokenFile string                      `json:"controller_token_file"`
	Workers             map[string]WorkerCredential `json:"workers"`
}
type Broker struct {
	Queue      *Queue
	Controller string
	Tokens     map[string]string
	Profiles   map[string][]string
}

func Token(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file must be regular and private (chmod 600)")
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 {
		return "", errors.New("credential must contain at least 32 characters")
	}
	return s, nil
}
func NewBroker(c BrokerConfig) (*Broker, error) {
	token, err := Token(c.ControllerTokenFile)
	if err != nil {
		return nil, err
	}
	b := &Broker{Controller: token, Tokens: map[string]string{}, Profiles: map[string][]string{}}
	used := map[string]bool{token: true}
	for name, w := range c.Workers {
		t, err := Token(w.TokenFile)
		if err != nil {
			return nil, err
		}
		if name == "" || used[t] || len(w.Profiles) == 0 {
			return nil, errors.New("worker names, unique scoped credentials and profiles required")
		}
		used[t] = true
		b.Tokens[name] = t
		b.Profiles[name] = w.Profiles
	}
	b.Queue, err = OpenQueue(c.StateDir)
	return b, err
}
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if !b.auth(w, r, b.Controller) {
			return
		}
		var j Job
		if !decode(w, r, &j) {
			return
		}
		profiles, ok := b.Profiles[j.Worker]
		if !ok || !contains(profiles, j.Profile) {
			http.Error(w, "unknown worker/profile", 400)
			return
		}
		out, err := b.Queue.Submit(j)
		reply(w, out, err)
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !b.auth(w, r, b.Controller) {
			return
		}
		j, err := b.Queue.Get(r.PathValue("id"))
		// A stale assignment is observationally uncertain, never eligible for redispatch.
		if err == nil && j.State == "assigned" && time.Since(j.Seen) > 30*time.Second {
			j.State = "disconnected"
			j.Warning = "execution outcome currently unknown; assignment retained, never automatically rerun"
		}
		reply(w, j, err)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !b.auth(w, r, b.Controller) {
			return
		}
		reply(w, struct{}{}, b.Queue.CancelJob(r.PathValue("id")))
	})
	mux.HandleFunc("POST /v1/jobs/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !b.auth(w, r, b.Controller) {
			return
		}
		reply(w, struct{}{}, b.Queue.Resolve(r.PathValue("id")))
	})
	mux.HandleFunc("POST /v1/workers/{worker}/poll", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("worker")
		if !b.auth(w, r, b.Tokens[name]) {
			return
		}
		j, err := b.Queue.Poll(name, r.Header.Get("X-Worker-Instance"))
		reply(w, j, err)
	})
	mux.HandleFunc("POST /v1/workers/{worker}/jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("worker")
		if !b.auth(w, r, b.Tokens[name]) {
			return
		}
		var out Result
		if !decode(w, r, &out) {
			return
		}
		reply(w, struct{}{}, b.Queue.Complete(r.PathValue("id"), name, r.Header.Get("X-Worker-Instance"), out))
	})
	mux.HandleFunc("PUT /v1/workers/{worker}/jobs/{id}/bundle", b.upload)
	mux.HandleFunc("GET /v1/jobs/{id}/bundle", func(w http.ResponseWriter, r *http.Request) {
		if !b.auth(w, r, b.Controller) {
			return
		}
		j, err := b.Queue.Get(r.PathValue("id"))
		if err != nil || j.BundleSHA == "" {
			http.Error(w, "no artifact", 404)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Content-SHA256", j.BundleSHA)
		http.ServeFile(w, r, filepath.Join(b.Queue.dir, j.ID+".tar.gz"))
	})
	return mux
}
func (b *Broker) auth(w http.ResponseWriter, r *http.Request, token string) bool {
	if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1 {
		return true
	}
	http.Error(w, "unauthorized", 401)
	return false
}
func (b *Broker) upload(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("worker"), r.PathValue("id")
	if !b.auth(w, r, b.Tokens[name]) {
		return
	}
	j, err := b.Queue.Get(id)
	if err != nil || j.Worker != name || j.Owner != r.Header.Get("X-Worker-Instance") || !validID(id) {
		http.Error(w, "unknown job", 404)
		return
	}
	f, err := os.CreateTemp(b.Queue.dir, ".bundle-*")
	if err != nil {
		reply(w, nil, err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), http.MaxBytesReader(w, r.Body, MaxBundle))
	if err != nil {
		http.Error(w, "artifact too large or interrupted", 400)
		return
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	if sha != r.Header.Get("X-Content-SHA256") {
		http.Error(w, "artifact checksum mismatch", 400)
		return
	}
	// Serialize blob publication with metadata so concurrent retries cannot overwrite it.
	b.Queue.mu.Lock()
	err = func() error {
		for i := range b.Queue.jobs {
			stored := &b.Queue.jobs[i]
			if stored.ID != id {
				continue
			}
			if stored.BundleSHA != "" {
				if stored.BundleSHA == sha && stored.BundleSize == n {
					return nil
				}
				return errors.New("artifact conflict")
			}
			if stored.State != "assigned" {
				return errors.New("job not assigned")
			}
			if err := f.Sync(); err != nil {
				return err
			}
			if err := os.Rename(f.Name(), filepath.Join(b.Queue.dir, id+".tar.gz")); err != nil {
				return err
			}
			return b.Queue.change(func() error { stored.BundleSHA = sha; stored.BundleSize = n; return nil })
		}
		return os.ErrNotExist
	}()
	b.Queue.mu.Unlock()
	reply(w, struct{}{}, err)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		http.Error(w, "invalid JSON", 400)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "trailing JSON", 400)
		return false
	}
	return true
}
func reply(w http.ResponseWriter, out any, err error) {
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type Client struct {
	Instance string
	URL      string
	Token    string
	HTTP     *http.Client
}

func NewClient(raw, token string) (*Client, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("verified HTTPS broker URL required")
	}
	return &Client{URL: strings.TrimRight(raw, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.Request(ctx, method, path, body, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
func (c *Client) Request(ctx context.Context, method, path string, body io.Reader, sha string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Worker-Instance", c.Instance)
	req.Header.Set("Content-Type", "application/json")
	if sha != "" {
		req.Header.Set("X-Content-SHA256", sha)
		req.Header.Set("Content-Type", "application/gzip")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("broker HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func workerPath(worker string) string { return "/v1/workers/" + url.PathEscape(worker) }
