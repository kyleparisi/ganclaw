package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Telegram's upload limits: 10 MB for photos, 50 MB for anything else.
const (
	maxPhotoBytes = 10 << 20
	maxFileBytes  = 50 << 20
)

// SendFile uploads a local file to a chat: images as photos, GIFs as
// animations, videos as videos and anything else as a document. A photo
// Telegram rejects (too large, odd dimensions) is resent as a document.
func (c *Client) SendFile(ctx context.Context, chatID int64, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > maxFileBytes {
		return fmt.Errorf("%s is %d MB; Telegram's limit is 50 MB", filepath.Base(path), fi.Size()>>20)
	}
	method, field := "sendDocument", "document"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		if fi.Size() <= maxPhotoBytes {
			method, field = "sendPhoto", "photo"
		}
	case ".gif":
		method, field = "sendAnimation", "animation"
	case ".mp4", ".mov", ".webm":
		method, field = "sendVideo", "video"
	}
	err = c.upload(ctx, method, field, chatID, path)
	var apiErr *APIError
	if method == "sendPhoto" && errors.As(err, &apiErr) && apiErr.Code == 400 {
		err = c.upload(ctx, "sendDocument", "document", chatID, path)
	}
	return err
}

// upload posts the file as multipart form data, streaming it from disk.
func (c *Client) upload(ctx context.Context, method, field string, chatID int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
		if err == nil {
			var part io.Writer
			if part, err = mw.CreateFormFile(field, filepath.Base(path)); err == nil {
				if _, err = io.Copy(part, f); err == nil {
					err = mw.Close()
				}
			}
		}
		pw.CloseWithError(err)
	}()
	err = c.post(ctx, method, mw.FormDataContentType(), pr, nil)
	pr.Close() // unblocks the writer if the request ended early
	return err
}
