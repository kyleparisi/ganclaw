// Package gemini integrates the Gemini CLI with the provider interface.
package gemini

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// event contains the fields used from Gemini CLI's stream-json protocol.
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamState struct {
	session  string
	text     strings.Builder
	status   string
	failure  string
	result   bool
	toolUsed bool
}

// parseStream consumes the complete output, including errors after a result.
func parseStream(r io.Reader, onDelta, onProgress func(string), onReset func()) (*streamState, error) {
	st := &streamState{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return st, err
		}
		switch e.Type {
		case "init":
			st.session = e.SessionID
		case "message":
			if e.Role == "assistant" && e.Content != "" {
				st.text.WriteString(e.Content)
				if onDelta != nil {
					onDelta(e.Content)
				}
			}
		case "tool_use":
			st.toolUsed = true
			if st.text.Len() > 0 {
				st.text.Reset()
				if onReset != nil {
					onReset()
				}
			}
			if onProgress != nil {
				onProgress("Tool activity observed")
			}
		case "error":
			if e.Severity == "error" {
				st.failure = e.Message
			}
		case "result":
			st.result = true
			st.status = e.Status
			if e.Error != nil && e.Error.Message != "" {
				st.failure = e.Error.Message
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return st, err
	}
	if !st.result {
		return st, errors.New("stream ended without a result event")
	}
	return st, nil
}
