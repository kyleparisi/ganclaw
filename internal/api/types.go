// Package api is ganclaw's local HTTP+JSON API, served on a Unix socket.
// Schedulers and scripts use it (usually through the CLI) to send messages
// and run agents.
package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

// SendRequest delivers a plain message, no agent involved.
type SendRequest struct {
	// To is a contact name or a raw address "telegram:<bot>:<chat>".
	To string `json:"to"`
	// Via picks the bot, overriding the contact's default_bot.
	Via            string `json:"via,omitempty"`
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type SendResponse struct {
	OK       bool   `json:"ok"`
	Target   string `json:"target"`
	Replayed bool   `json:"replayed,omitempty"`
}

// RunRequest runs one agent turn.
type RunRequest struct {
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
	// To, if set, streams the reply to that address and continues that
	// chat's conversation, so the recipient can reply with context.
	To  string `json:"to,omitempty"`
	Via string `json:"via,omitempty"`
	// Session names a conversation so later runs with the same name share
	// context. Without To, empty means a one-off run. With To, empty means
	// the chat's own conversation; a name keeps the work separate while
	// still delivering the reply to the chat.
	Session        string `json:"session,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Quiet delivers the reply without streaming, and not at all if the
	// agent answers exactly NO_REPLY (or HEARTBEAT_OK). Errors are still
	// delivered.
	Quiet bool `json:"quiet,omitempty"`
	// Heartbeat appends the tasks in the agent's HEARTBEAT.md, if any.
	Heartbeat bool `json:"heartbeat,omitempty"`
	// Async queues the run and returns immediately with queued=true.
	Async bool `json:"async,omitempty"`
}

type RunResponse struct {
	OK       bool   `json:"ok"`
	Provider string `json:"provider"`
	Text     string `json:"text"`
	Target   string `json:"target,omitempty"`
	Queued   bool   `json:"queued,omitempty"`
	Replayed bool   `json:"replayed,omitempty"`
}

// AgentsResponse lists what API callers can address.
type AgentsResponse struct {
	Agents   []AgentInfo `json:"agents"`
	Contacts []string    `json:"contacts"`
}

type AgentInfo struct {
	Name string `json:"name"`
	Bot  string `json:"bot,omitempty"` // bot that delivers this agent's replies
}

// HealthResponse is GET /v1/health: everything a monitor needs.
type HealthResponse struct {
	Version   string            `json:"version"`
	Providers []provider.Status `json:"providers"`
	Bots      []BotHealth       `json:"bots"`
	Probes    []ProbeResult     `json:"probes"`
	// ActiveTurns is how many agent turns are running, so maintenance can
	// wait for a quiet moment.
	ActiveTurns int `json:"active_turns"`
}

type BotHealth struct {
	Name     string    `json:"name"`
	Running  bool      `json:"running"`
	LastPoll time.Time `json:"last_poll"`
	LastErr  string    `json:"last_error,omitempty"`
}

// ProbeResult is one configured HTTP check, made by the gateway (as its
// service user) when health is requested.
type ProbeResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Error is an API error. Code is stable and machine-readable.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type errorResponse struct {
	OK    bool   `json:"ok"`
	Error *Error `json:"error"`
}

// Error codes.
const (
	CodeBadRequest     = "bad_request"
	CodeUnknownContact = "unknown_contact"
	CodeUnknownBot     = "unknown_bot"
	CodeUnknownAgent   = "unknown_agent"
	CodeInProgress     = "in_progress"
	CodeBusy           = "busy"
	CodeUnavailable    = "unavailable"
	CodeTimeout        = "timeout"
	CodeFailed         = "failed"
	CodeNoServer       = "no_server" // client side: the socket isn't reachable
)

var codeStatus = map[string]int{
	CodeBadRequest:     http.StatusBadRequest,
	CodeUnknownContact: http.StatusNotFound,
	CodeUnknownBot:     http.StatusNotFound,
	CodeUnknownAgent:   http.StatusNotFound,
	CodeInProgress:     http.StatusConflict,
	CodeBusy:           http.StatusTooManyRequests,
	CodeUnavailable:    http.StatusServiceUnavailable,
	CodeTimeout:        http.StatusGatewayTimeout,
	CodeFailed:         http.StatusInternalServerError,
}

// Errorf builds an Error with the status for its code.
func Errorf(code, format string, args ...any) *Error {
	status, ok := codeStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Target is a resolved address.
type Target struct {
	Channel string // "telegram"
	Bot     string
	Chat    int64
}

func (t Target) String() string { return fmt.Sprintf("%s:%s:%d", t.Channel, t.Bot, t.Chat) }

// ParseAddress parses "telegram:<bot>:<chat>".
func ParseAddress(s string) (Target, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "telegram" || parts[1] == "" {
		return Target{}, false
	}
	chat, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Target{}, false
	}
	return Target{Channel: "telegram", Bot: parts[1], Chat: chat}, true
}
