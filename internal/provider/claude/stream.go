package claude

import (
	"encoding/json"
	"io"
	"strings"
	"time"
)

// event is the subset of `claude -p --output-format stream-json` lines we
// read. Unknown types and fields are ignored.
type event struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Event           *streamEvent    `json:"event"`           // type=stream_event
	RateLimitInfo   *rateLimitInfo  `json:"rate_limit_info"` // type=rate_limit_event
	Result          string          `json:"result"`          // type=result
	IsError         bool            `json:"is_error"`
	APIErrorStatus  *int            `json:"api_error_status"`
	Errors          json.RawMessage `json:"errors"`
}

type streamEvent struct {
	Type         string `json:"type"` // content_block_start | content_block_delta | ...
	ContentBlock *struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta *struct {
		Type string `json:"type"` // text_delta | thinking_delta | ...
		Text string `json:"text"`
	} `json:"delta"`
}

type rateLimitInfo struct {
	Status         string                     `json:"status"` // allowed | allowed_warning | rejected
	RateLimitType  string                     `json:"rateLimitType"`
	ResetsAt       int64                      `json:"resetsAt"` // unix seconds
	UnifiedWindows map[string]rateLimitWindow `json:"unifiedWindows"`
}

type rateLimitWindow struct {
	Utilization float64 `json:"utilization"` // 0..1
	ResetsAt    int64   `json:"resetsAt"`
}

type streamState struct {
	session     string
	result      *event
	rateLimit   *rateLimitInfo // latest report of any status
	rateLimited *rateLimitInfo // latest rejected report
	wroteText   bool
}

// parseStream reads events until EOF, forwarding top-level answer text to
// onDelta. Text from sub-agents (non-null parent_tool_use_id) and thinking
// is not forwarded.
func parseStream(r io.Reader, onDelta func(string)) (*streamState, error) {
	st := &streamState{}
	dec := json.NewDecoder(r)
	for {
		var e event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				if st.result == nil {
					return st, errNoResult
				}
				return st, nil
			}
			return st, err
		}
		if e.SessionID != "" {
			st.session = e.SessionID
		}
		switch e.Type {
		case "stream_event":
			if e.Event == nil || e.ParentToolUseID != nil || onDelta == nil {
				continue
			}
			switch e.Event.Type {
			case "content_block_start":
				// Separate text blocks (e.g. before and after a tool call).
				if e.Event.ContentBlock != nil && e.Event.ContentBlock.Type == "text" && st.wroteText {
					onDelta("\n\n")
				}
			case "content_block_delta":
				if d := e.Event.Delta; d != nil && d.Type == "text_delta" && d.Text != "" {
					onDelta(d.Text)
					st.wroteText = true
				}
			}
		case "rate_limit_event":
			if e.RateLimitInfo != nil {
				st.rateLimit = e.RateLimitInfo
				if e.RateLimitInfo.Status == "rejected" {
					st.rateLimited = e.RateLimitInfo
				}
			}
		case "result":
			ev := e
			st.result = &ev
		}
	}
}

// err converts a failed result into an *Error, or returns nil on success.
func (st *streamState) err() error {
	r := st.result
	if r == nil {
		return &Error{Message: errNoResult.Error(), Unavailable: true}
	}
	if !r.IsError && r.Subtype == "success" {
		return nil
	}
	e := &Error{Subtype: r.Subtype, Message: strings.TrimSpace(r.Result)}
	if e.Message == "" && len(r.Errors) > 0 && string(r.Errors) != "null" {
		e.Message = string(r.Errors)
	}
	if r.APIErrorStatus != nil {
		e.Status = *r.APIErrorStatus
	}
	switch {
	case st.rateLimited != nil:
		e.Unavailable = true
		if e.Message == "" {
			e.Message = "rate limited (" + st.rateLimited.RateLimitType + ")"
		}
		if st.rateLimited.ResetsAt > 0 {
			e.Retry = time.Unix(st.rateLimited.ResetsAt, 0)
		}
	case e.Status == 401 || e.Status == 403 || e.Status == 429 || e.Status == 529 || e.Status >= 500:
		e.Unavailable = true
	}
	return e
}
