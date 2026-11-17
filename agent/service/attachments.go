package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

const (
	maxAttachments    = 10
	maxImageBytes     = 20 << 20
	maxTextFileBytes  = 256 << 10
	maxImageSide      = 1568
	minImageTokens    = 85
	attachmentTimeout = time.Minute
)

// The attachment is a copy taken when the message is sent.
type AttachmentInput struct {
	Path     string
	Name     string
	MimeType string
}

var imageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// Stored before the message goes through the loop: a slow sandbox or blob store there would hold up every
// thread on the shard.
func (sv *Service) attach(ctx context.Context, t db.ThreadRow, ins []AttachmentInput) ([]db.Attachment, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if sv.cfg.Attachments == nil {
		return nil, fmt.Errorf("service: %w: this agent takes no attachments", ErrInvalidMessage)
	}
	if len(ins) > maxAttachments {
		return nil, fmt.Errorf("service: %w: %d attachments; at most %d are accepted", ErrInvalidMessage, len(ins), maxAttachments)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	out := make([]db.Attachment, 0, len(ins))
	for _, in := range ins {
		att, err := sv.storeAttachment(ctx, t, in)
		if err != nil {
			sv.removeAttachments(ctx, out)
			return nil, err
		}
		out = append(out, att)
	}
	return out, nil
}

func (sv *Service) storeAttachment(ctx context.Context, t db.ThreadRow, in AttachmentInput) (db.Attachment, error) {
	if !filepath.IsAbs(in.Path) {
		return db.Attachment{}, fmt.Errorf("service: %w: attachment path %q is not absolute", ErrInvalidMessage, in.Path)
	}
	data, err := sv.attachmentSource(ctx, t.UserID, in.Path)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("service: %w: attachment %s: %v", ErrInvalidMessage, in.Path, err)
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = in.Path
	}
	att, err := describe(data, attachmentName(name), in.MimeType)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("service: %w: %v", ErrInvalidMessage, err)
	}
	att.File = t.ID + "/" + att.File
	if err := sv.cfg.Attachments.Put(ctx, att.File, data); err != nil {
		return db.Attachment{}, fmt.Errorf("service: store attachment %s: %w", att.Name, err)
	}
	return att, nil
}

// Only the user's sandbox: nothing else is theirs to send.
func (sv *Service) attachmentSource(ctx context.Context, user, path string) ([]byte, error) {
	box, err := sv.cfg.BoxFor(user)
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

// Classified by its first bytes, whatever its sender calls it.
func describe(data []byte, name, mime string) (db.Attachment, error) {
	if len(data) == 0 {
		return db.Attachment{}, fmt.Errorf("attachment %q is empty", name)
	}
	head := data[:min(len(data), 512)]
	sniffed, _, _ := strings.Cut(http.DetectContentType(head), ";")
	att := db.Attachment{ID: uuid.NewString(), Name: name, MimeType: sniffed, Size: int64(len(data))}
	if ext, ok := imageTypes[sniffed]; ok {
		att.Kind, att.File = db.AttachmentImage, att.ID+ext
		att.Width, att.Height = imageSize(bytes.NewReader(data), head)
		if att.Width <= 0 || att.Height <= 0 {
			return db.Attachment{}, fmt.Errorf("image %q cannot be read", name)
		}
		att.Tokens = imageTokens(att.Width, att.Height)
		return att, nil
	}
	notText := fmt.Errorf("%q (%s) is neither an image (PNG, JPEG, GIF, WebP) nor a text file", name, sniffed)
	if bytes.IndexByte(head, 0) >= 0 {
		return db.Attachment{}, notText
	}
	if len(data) > maxTextFileBytes {
		return db.Attachment{}, fmt.Errorf("text file %q has %d bytes; at most %d are accepted", name, len(data), maxTextFileBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return db.Attachment{}, notText
	}
	// Text is of the kind its sender calls it, which its bytes do not tell.
	if mime = strings.ToLower(strings.TrimSpace(mime)); mime != "" {
		att.MimeType = mime
	}
	if !strings.HasPrefix(att.MimeType, "text/") && att.MimeType != "application/json" {
		att.MimeType = "text/plain"
	}
	att.Kind, att.File = db.AttachmentFile, att.ID+".txt"
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

// head, the first bytes, is enough for WebP.
func imageSize(r io.Reader, head []byte) (int, int) {
	if cfg, _, err := image.DecodeConfig(r); err == nil {
		return cfg.Width, cfg.Height
	}
	return webpSize(head)
}

func webpSize(b []byte) (int, int) {
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8X":
		return 1 + int(b[24]) | int(b[25])<<8 | int(b[26])<<16, 1 + int(b[27]) | int(b[28])<<8 | int(b[29])<<16
	case "VP8 ":
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
	case "VP8L":
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1
	}
	return 0, 0
}

// About one token per 750 pixels of the image scaled to fit maxImageSide.
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

func (sv *Service) removeAttachments(ctx context.Context, atts []db.Attachment) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachmentTimeout)
	defer cancel()
	for _, att := range atts {
		sv.cfg.Attachments.Delete(ctx, att.File)
	}
}

func (sv *Service) ReadAttachment(ctx context.Context, user, threadID, id string) (db.Attachment, []byte, error) {
	if err := sv.owned(user, threadID); err != nil {
		return db.Attachment{}, nil, err
	}
	att, err := sv.cfg.DB.Attachment(user, threadID, id)
	if err != nil {
		return db.Attachment{}, nil, fmt.Errorf("service: attachment %s: %w", id, err)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	data, err := sv.cfg.Attachments.Get(ctx, att.File)
	if err != nil {
		return db.Attachment{}, nil, fmt.Errorf("service: attachment %s: %w", id, err)
	}
	return att, data, nil
}

func (sv *Service) checkImages(threadID string, model llm.Model, atts []db.Attachment) error {
	if model.Images {
		return nil
	}
	for _, att := range atts {
		if att.Kind == db.AttachmentImage {
			return fmt.Errorf("service: %w: model %s does not accept images; pick a model that does", ErrInvalidMessage, model.Name)
		}
	}
	has, err := sv.cfg.DB.HasImages(threadID)
	if err != nil {
		return fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	if has {
		return fmt.Errorf("service: %w: this conversation shows the model images, and model %s does not accept them; pick a model that does", ErrInvalidMessage, model.Name)
	}
	return nil
}
