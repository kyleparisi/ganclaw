package codex

import "encoding/json"

// Hand-written subset of the codex app-server protocol. The full JSON Schema
// lives in ./schema (regenerate with `codex app-server generate-json-schema`).

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type initializeResponse struct {
	CodexHome string `json:"codexHome"`
	UserAgent string `json:"userAgent"`
}

type threadParams struct {
	ThreadID              string `json:"threadId,omitempty"` // resume only
	Cwd                   string `json:"cwd,omitempty"`
	Model                 string `json:"model,omitempty"`
	DeveloperInstructions string `json:"developerInstructions,omitempty"`
	ApprovalPolicy        string `json:"approvalPolicy,omitempty"` // untrusted | on-request | never
	Sandbox               string `json:"sandbox,omitempty"`        // read-only | workspace-write | danger-full-access
	ServiceName           string `json:"serviceName,omitempty"`    // start only
	ExcludeTurns          bool   `json:"excludeTurns,omitempty"`   // resume only
}

type threadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model string `json:"model"`
}

type userInput struct {
	Type string `json:"type"`           // "text" | "localImage"
	Text string `json:"text,omitempty"` // text
	Path string `json:"path,omitempty"` // localImage
}

type turnStartParams struct {
	ThreadID string      `json:"threadId"`
	Input    []userInput `json:"input"`
}

type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type turn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"` // completed | interrupted | failed | inProgress
	Error  *turnError `json:"error"`
}

type turnStartResponse struct {
	Turn turn `json:"turn"`
}

type turnError struct {
	Message           string          `json:"message"`
	AdditionalDetails string          `json:"additionalDetails"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
}

// Code returns the error code: either the plain string form
// ("usageLimitExceeded") or the single key of the object form
// ({"httpConnectionFailed": {...}}).
func (e *turnError) Code() string {
	if e == nil || len(e.CodexErrorInfo) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.CodexErrorInfo, &s) == nil {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(e.CodexErrorInfo, &m) == nil {
		for k := range m {
			return k
		}
	}
	return ""
}

// account/rateLimits/read

type rateLimitsResponse struct {
	OrdinaryUsageAllowed *bool             `json:"ordinaryUsageAllowed"`
	RateLimits           rateLimitSnapshot `json:"rateLimits"`
}

type rateLimitSnapshot struct {
	LimitID              string           `json:"limitId"`
	Primary              *rateLimitWindow `json:"primary"`
	Secondary            *rateLimitWindow `json:"secondary"`
	PlanType             string           `json:"planType"`
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
	Credits              *struct {
		HasCredits bool   `json:"hasCredits"`
		Unlimited  bool   `json:"unlimited"`
		Balance    string `json:"balance"`
	} `json:"credits"`
}

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"` // unix seconds
}

// Notifications.

type agentMessageDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type itemNotification struct {
	ThreadID string     `json:"threadId"`
	TurnID   string     `json:"turnId"`
	Item     threadItem `json:"item"`
}

type threadItem struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text"`  // agentMessage
	Phase string `json:"phase"` // agentMessage: commentary | final_answer | "" (unknown)
}

type turnCompleted struct {
	ThreadID string `json:"threadId"`
	Turn     turn   `json:"turn"`
}

type errorNotification struct {
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	Error     turnError `json:"error"`
	WillRetry bool      `json:"willRetry"`
}
