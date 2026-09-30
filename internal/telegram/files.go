package telegram

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/attachments"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

// fileRef is a downloadable attachment on a message.
type fileRef struct {
	FileID   string
	Name     string
	MimeType string
	Kind     string
	Size     int64
}

// fileRefs lists a message's attachments. Photos use the largest size.
func fileRefs(m *Message) []fileRef {
	var refs []fileRef
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1]
		refs = append(refs, fileRef{FileID: p.FileID, Name: "photo.jpg", MimeType: "image/jpeg", Kind: provider.KindImage, Size: p.FileSize})
	}
	if d := m.Document; d != nil {
		refs = append(refs, fileRef{FileID: d.FileID, Name: nameOr(d.FileName, "file"), MimeType: d.MimeType, Kind: kindOf(d.MimeType), Size: d.FileSize})
	}
	if v := m.Voice; v != nil {
		refs = append(refs, fileRef{FileID: v.FileID, Name: "voice.ogg", MimeType: nameOr(v.MimeType, "audio/ogg"), Kind: provider.KindAudio, Size: v.FileSize})
	}
	if a := m.Audio; a != nil {
		refs = append(refs, fileRef{FileID: a.FileID, Name: nameOr(a.FileName, "audio"), MimeType: a.MimeType, Kind: provider.KindAudio, Size: a.FileSize})
	}
	if v := m.Video; v != nil {
		refs = append(refs, fileRef{FileID: v.FileID, Name: nameOr(v.FileName, "video.mp4"), MimeType: v.MimeType, Kind: provider.KindFile, Size: v.FileSize})
	}
	return refs
}

func kindOf(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return provider.KindImage
	case strings.HasPrefix(mime, "audio/"):
		return provider.KindAudio
	default:
		return provider.KindFile
	}
}

func nameOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// fetch downloads refs into dir.
func (c *Client) fetch(ctx context.Context, refs []fileRef, dir string) ([]provider.Attachment, error) {
	var out []provider.Attachment
	for _, r := range refs {
		if r.Size > MaxDownload {
			return nil, tooBig(r.Name, r.Size)
		}
		f, err := c.GetFile(ctx, r.FileID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Name, err)
		}
		if f.FileSize > MaxDownload {
			return nil, tooBig(r.Name, f.FileSize)
		}
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(c.Download(ctx, f.FilePath, pw)) }()
		path, n, err := attachments.Save(dir, r.Name, pr, MaxDownload)
		pr.CloseWithError(io.ErrClosedPipe) // stop the download if Save gave up early
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Name, err)
		}
		out = append(out, provider.Attachment{Path: path, Name: r.Name, MimeType: r.MimeType, Kind: r.Kind, Size: n})
	}
	return out, nil
}

func tooBig(name string, size int64) error {
	return fmt.Errorf("%s is %.1f MB; Telegram only lets bots download files up to 20 MB", name, float64(size)/(1<<20))
}
