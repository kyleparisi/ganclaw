package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

// Client talks to the API over its Unix socket.
type Client struct {
	Socket string
	HTTP   *http.Client
}

func NewClient(socket string) *Client {
	return &Client{Socket: socket, HTTP: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}}
}

func (c *Client) Send(ctx context.Context, req SendRequest) (SendResponse, error) {
	var resp SendResponse
	err := c.do(ctx, http.MethodPost, "/v1/send", req, &resp)
	return resp, err
}

func (c *Client) Run(ctx context.Context, req RunRequest) (RunResponse, error) {
	var resp RunResponse
	err := c.do(ctx, http.MethodPost, "/v1/run", req, &resp)
	return resp, err
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var resp HealthResponse
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, &resp)
	return resp, err
}

func (c *Client) Agents(ctx context.Context) (AgentsResponse, error) {
	var resp AgentsResponse
	err := c.do(ctx, http.MethodGet, "/v1/agents", nil, &resp)
	return resp, err
}

func (c *Client) Status(ctx context.Context) ([]provider.Status, error) {
	var resp struct {
		Providers []provider.Status `json:"providers"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &resp)
	return resp.Providers, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://ganclaw"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Code: CodeNoServer, Message: fmt.Sprintf("can't reach ganclaw at %s: %v", c.Socket, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var er errorResponse
		if json.Unmarshal(data, &er) == nil && er.Error != nil {
			er.Error.Status = resp.StatusCode
			return er.Error
		}
		return &Error{Status: resp.StatusCode, Code: CodeFailed, Message: string(bytes.TrimSpace(data))}
	}
	return json.Unmarshal(data, out)
}
