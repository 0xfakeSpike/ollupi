package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

const (
	imageKind = "image"
	fileKind  = "file"
)

const (
	maxAttachments    = 10
	maxImageBytes     = 20 << 20
	maxTextFileBytes  = 256 << 10
	maxImageSide      = 1568
	minImageTokens    = 85
	attachmentTimeout = time.Minute
)

type AttachmentInput struct {
	Path     string
	Name     string
	MimeType string
}

var imageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func (a *App) attach(ctx context.Context, u, thread string, ins []AttachmentInput) ([]conversation.Attachment, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if len(ins) > maxAttachments {
		return nil, fmt.Errorf("%w: %d attachments; at most %d are accepted", ErrInvalidMessage, len(ins), maxAttachments)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	out := make([]conversation.Attachment, 0, len(ins))
	for _, in := range ins {
		att, err := a.keepAttachment(ctx, u, thread, in)
		if err != nil {
			a.detach(ctx, out)
			return nil, err
		}
		out = append(out, att)
	}
	return out, nil
}

func (a *App) keepAttachment(ctx context.Context, u, thread string, in AttachmentInput) (conversation.Attachment, error) {
	if !filepath.IsAbs(in.Path) {
		return conversation.Attachment{}, fmt.Errorf("%w: attachment path %q is not absolute", ErrInvalidMessage, in.Path)
	}
	data, err := a.attachmentSource(ctx, u, in.Path)
	if err != nil {
		return conversation.Attachment{}, fmt.Errorf("%w: attachment %s: %v", ErrInvalidMessage, in.Path, err)
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = in.Path
	}
	att, err := describe(data, attachmentName(name), in.MimeType)
	if err != nil {
		return conversation.Attachment{}, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	att.File = thread + "/" + att.File
	if err := a.attachments.Put(ctx, att.File, data); err != nil {
		return conversation.Attachment{}, fmt.Errorf("store attachment %s: %w", att.Name, err)
	}
	return att, nil
}

func (a *App) attachmentSource(ctx context.Context, u, path string) ([]byte, error) {
	box, err := a.boxes(u)
	if err != nil {
		return nil, err
	}
	info, err := box.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a file")
	}
	if info.Size() > maxImageBytes {
		return nil, fmt.Errorf("%d bytes; at most %d are accepted", info.Size(), maxImageBytes)
	}
	return sandbox.ReadFile(ctx, box, path, maxImageBytes)
}

func describe(data []byte, name, mime string) (conversation.Attachment, error) {
	if len(data) == 0 {
		return conversation.Attachment{}, fmt.Errorf("attachment %q is empty", name)
	}
	head := data[:min(len(data), 512)]
	sniffed, _, _ := strings.Cut(http.DetectContentType(head), ";")
	att := conversation.Attachment{ID: uuid.NewString(), Name: name, MimeType: sniffed, Size: int64(len(data))}
	if ext, ok := imageTypes[sniffed]; ok {
		att.Kind, att.File = imageKind, att.ID+ext
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return conversation.Attachment{}, fmt.Errorf("image %q cannot be read", name)
		}
		att.Width, att.Height = cfg.Width, cfg.Height
		att.Tokens = imageTokens(att.Width, att.Height)
		return att, nil
	}
	notText := fmt.Errorf("%q (%s) is neither an image (PNG, JPEG, GIF, WebP) nor a text file", name, sniffed)
	if bytes.IndexByte(head, 0) >= 0 {
		return conversation.Attachment{}, notText
	}
	if len(data) > maxTextFileBytes {
		return conversation.Attachment{}, fmt.Errorf("text file %q has %d bytes; at most %d are accepted", name, len(data), maxTextFileBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return conversation.Attachment{}, notText
	}
	if mime = strings.ToLower(strings.TrimSpace(mime)); mime != "" {
		att.MimeType = mime
	}
	if !strings.HasPrefix(att.MimeType, "text/") && att.MimeType != "application/json" {
		att.MimeType = "text/plain"
	}
	att.Kind, att.File = fileKind, att.ID+".txt"
	att.Tokens = llm.EstimateTokens(string(data)) + llm.EstimateTokens(name) + 8
	return att, nil
}

func attachmentName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == "/" {
		return "attachment"
	}
	return clipRunes(name, 120)
}

func imageTokens(w, h int) int {
	if long := max(w, h); long > maxImageSide {
		w, h = w*maxImageSide/long, h*maxImageSide/long
	}
	return max(minImageTokens, w*h/750)
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (a *App) detach(ctx context.Context, atts []conversation.Attachment) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachmentTimeout)
	defer cancel()
	for _, att := range atts {
		a.attachments.Delete(ctx, att.File)
	}
}

func (a *App) ReadAttachment(ctx context.Context, u, threadID, id string) (conversation.Attachment, []byte, error) {
	var att *conversation.Attachment
	err := a.st.Read(ctx, func(r store.Reader) error {
		v, err := view(r, u, threadID)
		if err != nil {
			return err
		}
		named := func(x conversation.Attachment) bool { return x.ID == id }
		for _, it := range v.Items {
			if i := slices.IndexFunc(it.Attachments, named); i >= 0 {
				att = &it.Attachments[i]
			}
		}
		return nil
	})
	if err != nil {
		return conversation.Attachment{}, nil, err
	}
	if att == nil {
		return conversation.Attachment{}, nil, fmt.Errorf("attachment %s: %w", id, store.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	data, err := a.attachments.Get(ctx, att.File)
	if err != nil {
		return conversation.Attachment{}, nil, fmt.Errorf("attachment %s: %w", id, err)
	}
	return *att, data, nil
}
