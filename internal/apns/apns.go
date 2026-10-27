// Package apns sends privacy-preserving alerts over Apple's HTTP/2 provider API.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/store"
)

type Config struct {
	KeyFile     string `toml:"key_file"`
	KeyID       string `toml:"key_id"`
	TeamID      string `toml:"team_id"`
	Topic       string `toml:"topic"`
	Environment string `toml:"environment"` // sandbox or production; explicit when enabled
}

// Client's side effects can be replaced inline in tests. Never log requests:
// their path and Authorization header contain credentials.
type Client struct {
	Do     func(*http.Request) (*http.Response, error)
	Now    func() time.Time
	Sign   func([]byte) ([]byte, error)
	Config Config
	mu     sync.Mutex
	jwt    string
	issued time.Time
}

func New(c Config) (*Client, error) {
	if c.KeyFile == "" && c.KeyID == "" && c.TeamID == "" && c.Topic == "" && c.Environment == "" {
		return nil, nil
	}
	if c.KeyFile == "" || c.KeyID == "" || c.TeamID == "" || c.Topic == "" || (c.Environment != "sandbox" && c.Environment != "production") {
		return nil, errors.New("APNs requires key_file, key_id, team_id, topic and environment (sandbox or production)")
	}
	data, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return nil, errors.New("cannot read APNs key file")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid APNs key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid APNs PKCS8 key")
	}
	k, ok := key.(*ecdsa.PrivateKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("APNs key must be P-256")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{Config: c, Do: client.Do, Now: time.Now, Sign: func(digest []byte) ([]byte, error) {
		r, s, err := ecdsa.Sign(rand.Reader, k, digest)
		if err != nil {
			return nil, err
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return sig, nil
	}}, nil
}

func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if c.jwt != "" && now.Sub(c.issued) >= 0 && now.Sub(c.issued) < 50*time.Minute {
		return c.jwt, nil
	}
	enc := base64.RawURLEncoding.EncodeToString
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.Config.KeyID})
	p, _ := json.Marshal(map[string]any{"iss": c.Config.TeamID, "iat": now.Unix()})
	unsigned := enc(h) + "." + enc(p)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := c.Sign(digest[:])
	if err != nil {
		return "", err
	}
	c.jwt = unsigned + "." + enc(sig)
	c.issued = now
	return c.jwt, nil
}

// AlertText says which agent (and job) a notification is about, never what
// the reply says: agent and job names come from the config, not from chats.
func AlertText(m store.AppMessage) string {
	switch {
	case m.Job != "" && m.Status == store.StatusError:
		return m.Thread + ": " + m.Job + " failed."
	case m.Job != "":
		return m.Thread + ": " + m.Job + " finished."
	case m.Status == store.StatusError:
		return m.Thread + " ran into a problem."
	default:
		return m.Thread + " replied."
	}
}

// Send reports permanent token invalidation separately from transient failures.
// No automatic retry: an ambiguous network failure may already have delivered.
func (c *Client) Send(ctx context.Context, device store.AppPush, m store.AppMessage) (bool, error) {
	jwt, err := c.token()
	if err != nil {
		return false, errors.New("APNs signing failed")
	}
	host := "https://api.push.apple.com"
	if c.Config.Environment == "sandbox" {
		host = "https://api.sandbox.push.apple.com"
	}
	body, _ := json.Marshal(map[string]any{"aps": map[string]any{"alert": map[string]string{"title": "Ganclaw", "body": AlertText(m)}, "sound": "default"}, "thread": m.Thread})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+device.Token, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("APNs request failed")
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.Config.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", "0")
	req.Header.Set("apns-collapse-id", fmt.Sprintf("reply-%d", m.ID))
	req.Header.Set("content-type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return false, errors.New("APNs transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, nil
	}
	var result struct {
		Reason    string `json:"reason"`
		Timestamp int64  `json:"timestamp"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result)
	invalid := (resp.StatusCode == 410 && (result.Timestamp == 0 || result.Timestamp >= device.Registered)) || (resp.StatusCode == 400 && (result.Reason == "BadDeviceToken" || result.Reason == "DeviceTokenNotForTopic"))
	return invalid, fmt.Errorf("APNs rejected notification (HTTP %d)", resp.StatusCode)
}
