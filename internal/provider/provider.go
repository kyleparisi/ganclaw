// Package provider defines the interface every model backend implements.
//
// Backends are long-lived subprocesses (codex app-server, claude CLI) that
// own their own login and conversation state. ganclaw only tracks which
// backend session belongs to which chat.
package provider

import (
	"context"
	"errors"
)

// Request is one user turn sent to a backend.
type Request struct {
	// Session is the backend's conversation ID from a previous Result.
	// Empty starts a new conversation.
	Session string
	// Cwd is the agent's workspace directory.
	Cwd string
	// Instructions is the agent's system/developer prompt. Only applied
	// when a new session is started.
	Instructions string
	// Prompt is the user's message, already mentioning any attachments.
	Prompt string
	// Attachments are files the user sent, saved inside Cwd. Providers
	// that support it (codex images) attach them natively as well.
	Attachments []Attachment
	// OnDelta, if set, receives streamed text of the final answer.
	OnDelta func(text string)
	// OnReset, if set, is called by Chain before falling back to the next
	// provider, so streamed text from the failed one can be discarded.
	OnReset func()
	// Settings are the agent's per-provider options.
	Settings Settings
}

// Settings are per-agent provider options. Zero values mean the provider's
// configured defaults.
type Settings struct {
	CodexModel     string
	CodexSandbox   string // read-only | workspace-write | danger-full-access
	ClaudeModel    string
	PermissionMode string // claude --permission-mode
}

// Attachment kinds.
const (
	KindImage = "image"
	KindAudio = "audio"
	KindFile  = "file"
)

// Attachment is a file the user sent with a message.
type Attachment struct {
	Path     string // absolute path on disk
	Name     string // original file name
	MimeType string
	Kind     string // KindImage, KindAudio or KindFile
	Size     int64
	// Transcript is the spoken text of an audio attachment, if it was
	// transcribed.
	Transcript string
	// TranscriptErr says why transcription failed, if it was attempted.
	TranscriptErr string
}

// Result is the outcome of a completed turn.
type Result struct {
	Session string
	Text    string
}

// Provider is a model backend.
type Provider interface {
	Name() string
	Run(ctx context.Context, req Request) (Result, error)
	Close() error
}

// ErrUnavailable marks errors where retrying on a different provider makes
// sense: usage or rate limits, overload, expired login, connection failures,
// or a crashed backend process.
var ErrUnavailable = errors.New("provider unavailable")
