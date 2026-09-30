package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

func TestFileRefs(t *testing.T) {
	subject := fileRefs

	t.Run("Photo uses the largest size", func(t *testing.T) {
		got := subject(&Message{Photo: []PhotoSize{{FileID: "small", FileSize: 10}, {FileID: "large", FileSize: 900}}})

		assert.Equal(t, []fileRef{{FileID: "large", Name: "photo.jpg", MimeType: "image/jpeg", Kind: provider.KindImage, Size: 900}}, got)
	})

	t.Run("Documents are classified by MIME type", func(t *testing.T) {
		img := subject(&Message{Document: &FileRef{FileID: "d1", FileName: "scan.png", MimeType: "image/png"}})
		pdf := subject(&Message{Document: &FileRef{FileID: "d2", FileName: "", MimeType: "application/pdf"}})

		assert.Equal(t, provider.KindImage, img[0].Kind)
		assert.Equal(t, "scan.png", img[0].Name)
		assert.Equal(t, provider.KindFile, pdf[0].Kind)
		assert.Equal(t, "file", pdf[0].Name)
	})

	t.Run("Voice, audio and video", func(t *testing.T) {
		got := subject(&Message{
			Voice: &FileRef{FileID: "v"},
			Audio: &FileRef{FileID: "a", FileName: "song.mp3", MimeType: "audio/mpeg"},
			Video: &FileRef{FileID: "m", MimeType: "video/mp4"},
		})

		require.Len(t, got, 3)
		assert.Equal(t, fileRef{FileID: "v", Name: "voice.ogg", MimeType: "audio/ogg", Kind: provider.KindAudio}, got[0])
		assert.Equal(t, provider.KindAudio, got[1].Kind)
		assert.Equal(t, fileRef{FileID: "m", Name: "video.mp4", MimeType: "video/mp4", Kind: provider.KindFile}, got[2])
	})

	t.Run("Text-only message has no files", func(t *testing.T) {
		assert.Empty(t, subject(&Message{Text: "hi"}))
	})
}

// fileServer fakes getFile and file downloads.
func fileServer(t *testing.T, files map[string]string) HTTPClient {
	return HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/getFile") {
			var body struct {
				FileID string `json:"file_id"`
			}
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			content, ok := files[body.FileID]
			if !ok {
				return &http.Response{StatusCode: 400, Body: jsonBody(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: invalid file_id"})}, nil
			}
			return okResponse(File{FileID: body.FileID, FileSize: int64(len(content)), FilePath: "docs/" + body.FileID}), nil
		}
		assert.Equal(t, http.MethodGet, req.Method)
		assert.True(t, strings.HasPrefix(req.URL.Path, "/file/bot"+testToken+"/docs/"))
		id := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(files[id]))}, nil
	}}
}

func TestClientFetch(t *testing.T) {
	ctx := context.Background()

	t.Run("Downloads each file into the directory", func(t *testing.T) {
		dir := t.TempDir()
		subject := &Client{Token: testToken, HTTP: fileServer(t, map[string]string{"f1": "PNGDATA", "f2": "%PDF"})}

		got, err := subject.fetch(ctx, []fileRef{
			{FileID: "f1", Name: "photo.jpg", MimeType: "image/jpeg", Kind: provider.KindImage},
			{FileID: "f2", Name: "report.pdf", MimeType: "application/pdf", Kind: provider.KindFile},
		}, dir)

		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, provider.KindImage, got[0].Kind)
		assert.Equal(t, int64(7), got[0].Size)
		assert.True(t, strings.HasSuffix(got[1].Path, "-report.pdf"))
		data, err := os.ReadFile(got[0].Path)
		require.NoError(t, err)
		assert.Equal(t, "PNGDATA", string(data))
	})

	t.Run("Files over 20 MB are refused before downloading", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			t.Error("must not call Telegram for an oversized file")
			return nil, errors.New("unexpected")
		}}}

		_, err := subject.fetch(ctx, []fileRef{{FileID: "big", Name: "movie.mp4", Size: 25 << 20}}, t.TempDir())

		assert.ErrorContains(t, err, "movie.mp4 is 25.0 MB")
		assert.ErrorContains(t, err, "up to 20 MB")
	})

	t.Run("getFile errors name the file", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: fileServer(t, map[string]string{})}

		_, err := subject.fetch(ctx, []fileRef{{FileID: "gone", Name: "old.pdf"}}, t.TempDir())

		assert.ErrorContains(t, err, "old.pdf")
		assert.ErrorContains(t, err, "invalid file_id")
	})

	t.Run("Download errors never contain the token", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/getFile") {
				return okResponse(File{FileID: "f", FilePath: "docs/f"}), nil
			}
			return nil, errors.New(`Get "` + req.URL.String() + `": connection reset`)
		}}}

		_, err := subject.fetch(ctx, []fileRef{{FileID: "f", Name: "a.txt"}}, t.TempDir())

		require.Error(t, err)
		assert.NotContains(t, err.Error(), testToken)
	})
}
