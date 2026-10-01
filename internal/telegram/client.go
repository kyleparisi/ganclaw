// Package telegram is a minimal Telegram Bot API client and long-polling
// bot that feeds the router.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// HTTPClient performs requests. Tests replace Do with an inline fake.
type HTTPClient struct {
	Do func(req *http.Request) (*http.Response, error)
}

// DefaultHTTP allows for long polls up to ~80s.
var DefaultHTTP = HTTPClient{Do: (&http.Client{Timeout: 90 * time.Second}).Do}

type Client struct {
	Token   string
	BaseURL string // default https://api.telegram.org
	HTTP    HTTPClient
}

type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type Message struct {
	MessageID    int64       `json:"message_id"`
	From         *User       `json:"from"`
	Chat         Chat        `json:"chat"`
	Text         string      `json:"text"`
	Caption      string      `json:"caption"`
	MediaGroupID string      `json:"media_group_id"`
	Photo        []PhotoSize `json:"photo"`
	Document     *FileRef    `json:"document"`
	Voice        *FileRef    `json:"voice"`
	Audio        *FileRef    `json:"audio"`
	Video        *FileRef    `json:"video"`
	// ReplyToMessage is the message this one answers, without its own
	// reply; Quote is the part of it the user highlighted, if any.
	ReplyToMessage *Message `json:"reply_to_message"`
	Quote          *Quote   `json:"quote"`
}

type Quote struct {
	Text string `json:"text"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// FileRef covers the fields shared by documents, voice notes, audio and
// video.
type FileRef struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// File is getFile's result.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

// MaxDownload is the Bot API's limit for downloading files.
const MaxDownload = 20 << 20

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// APIError is an error response from the Bot API.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// MaxMessageLen is Telegram's limit for sendMessage text, in characters.
const MaxMessageLen = 4096

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for new messages starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	}, &ups)
	return ups, err
}

// SendMessage sends Markdown text, split into as many messages as needed.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	for _, part := range SplitMarkdown(text, MaxMessageLen) {
		if _, err := c.SendOne(ctx, chatID, part); err != nil {
			return err
		}
	}
	return nil
}

// SendOne sends a single Markdown message (at most MaxMessageLen
// characters) and returns its ID.
func (c *Client) SendOne(ctx context.Context, chatID int64, text string) (int64, error) {
	var m Message
	err := c.callFormatted(ctx, "sendMessage", map[string]any{"chat_id": chatID}, text, &m)
	return m.MessageID, err
}

// EditMessageText replaces a message's text with Markdown text. Editing to
// identical text is not an error.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	err := c.callFormatted(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID}, text, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// GetFile resolves a file_id to a downloadable path.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

// Download streams a file from GetFile's FilePath into w.
func (c *Client) Download(ctx context.Context, filePath string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/file/bot"+c.Token+"/"+filePath, nil)
	if err != nil {
		return c.redact(err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram download: HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return c.redact(err)
}

// BotCommand is an entry in the bot's "/" command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands sets the command menu shown when typing "/".
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// callFormatted sends text rendered from Markdown to HTML. If Telegram
// can't parse the result, it sends the text as written instead.
func (c *Client) callFormatted(ctx context.Context, method string, params map[string]any, text string, result any) error {
	params["text"] = RenderMarkdown(text)
	params["parse_mode"] = "HTML"
	err := c.call(ctx, method, params, result)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Description, "can't parse entities") {
		return err
	}
	params["text"] = text
	delete(params, "parse_mode")
	return c.call(ctx, method, params, result)
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.post(ctx, method, "application/json", bytes.NewReader(body), result)
}

func (c *Client) post(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/bot"+c.Token+"/"+method, body)
	if err != nil {
		return c.redact(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return c.redact(err)
	}

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: invalid response: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Method: method, Code: code, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if result != nil {
		return json.Unmarshal(env.Result, result)
	}
	return nil
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return "https://api.telegram.org"
	}
	return c.BaseURL
}

func (c *Client) httpClient() HTTPClient {
	if c.HTTP.Do == nil {
		return DefaultHTTP
	}
	return c.HTTP
}

// redact removes the bot token from errors, which net/http includes via the
// request URL.
func (c *Client) redact(err error) error {
	if err == nil || c.Token == "" || !strings.Contains(err.Error(), c.Token) {
		return err
	}
	return redactedError{msg: strings.ReplaceAll(err.Error(), c.Token, "<token>"), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }

// Is keeps context errors matchable without exposing the wrapped message.
func (e redactedError) Is(target error) bool {
	return (target == context.Canceled || target == context.DeadlineExceeded) && errors.Is(e.err, target)
}

// SplitText splits s into chunks of at most max characters, preferring to
// break at a newline, then a space.
func SplitText(s string, max int) []string {
	var parts []string
	for utf8.RuneCountInString(s) > max {
		cut := byteIndexOfRune(s, max)
		if i := strings.LastIndex(s[:cut], "\n"); i > 0 {
			cut = i + 1
		} else if i := strings.LastIndex(s[:cut], " "); i > 0 {
			cut = i + 1
		}
		parts = append(parts, s[:cut])
		// A message can't usefully start with the newline we split beside.
		s = strings.TrimLeft(s[cut:], "\n")
	}
	if s != "" || len(parts) == 0 {
		parts = append(parts, s)
	}
	return parts
}

func byteIndexOfRune(s string, n int) int {
	i := 0
	for j := range s {
		if i == n {
			return j
		}
		i++
	}
	return len(s)
}
