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
	"log/slog"
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
	defaultImageToken = 1600
	attachmentTimeout = time.Minute
)

// AttachmentInput is a file in the user's sandbox sent with a message; the attachment is a copy taken when it is sent.
type AttachmentInput struct {
	Path     string
	Name     string
	MimeType string
}

var imageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// attach stores what a message brings, before the message goes through the loop: a sandbox or blob store that is slow
// there would hold up every thread on the shard.
func (sv *Service) attach(ctx context.Context, user, threadID string, ins []AttachmentInput) ([]db.Attachment, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	return sv.storeAttachments(ctx, t, ins)
}

func (sv *Service) storeAttachments(ctx context.Context, t db.ThreadRow, ins []AttachmentInput) ([]db.Attachment, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if sv.cfg.Attachments == nil {
		return nil, fmt.Errorf("service: %w: this agent takes no attachments", ErrInvalidMessage)
	}
	if len(ins) > maxAttachments {
		return nil, fmt.Errorf("service: %w: %d attachments; at most %d are accepted", ErrInvalidMessage, len(ins), maxAttachments)
	}
	out := make([]db.Attachment, 0, len(ins))
	for _, in := range ins {
		att, err := sv.storeAttachment(ctx, t, in)
		if err != nil {
			sv.removeAttachments(out)
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
	src, size, err := sv.attachmentSource(ctx, t.UserID, in.Path)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("service: %w: attachment %s: %v", ErrInvalidMessage, in.Path, err)
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = in.Path
	}
	att, err := describe(src, size, attachmentName(name), in.MimeType)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("service: %w: %v", ErrInvalidMessage, err)
	}
	att.File = t.ID + "/" + att.File
	if err := sv.copyAttachment(ctx, src, att); err != nil {
		return db.Attachment{}, fmt.Errorf("service: store attachment %s: %w", att.Name, err)
	}
	return att, nil
}

// attachmentSource reads the file a user names, from their sandbox: nothing else is theirs to send.
func (sv *Service) attachmentSource(ctx context.Context, user, path string) (io.ReadSeeker, int64, error) {
	box, err := sv.cfg.BoxFor(user)
	if err != nil {
		return nil, 0, err
	}
	info, err := box.Stat(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("not a file")
	}
	if info.Size() > maxImageBytes {
		return nil, 0, fmt.Errorf("%d bytes; at most %d are accepted", info.Size(), maxImageBytes)
	}
	data, err := sandbox.ReadFile(ctx, box, path, maxImageBytes)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(data), int64(len(data)), nil
}

// copyAttachment copies exactly the bytes describe measured, so a file that changes meanwhile is refused.
func (sv *Service) copyAttachment(ctx context.Context, src io.ReadSeeker, att db.Attachment) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(src, att.Size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != att.Size {
		return fmt.Errorf("%w: the file changed while it was being attached", ErrInvalidMessage)
	}
	return sv.cfg.Attachments.Put(ctx, att.File, data)
}

// describe classifies a file as an image or a text file from its first bytes and checks it against the limits
// of its kind before reading more of it.
func describe(src io.ReadSeeker, size int64, name, mime string) (db.Attachment, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return db.Attachment{}, fmt.Errorf("attachment %q cannot be read: %w", name, err)
	}
	head = head[:n]
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "" {
		mime, _, _ = strings.Cut(http.DetectContentType(head), ";")
	}
	att := db.Attachment{ID: uuid.NewString(), Name: name, MimeType: mime, Size: size}
	if size == 0 {
		return db.Attachment{}, fmt.Errorf("attachment %q is empty", name)
	}
	if ext, ok := imageTypes[mime]; ok {
		if size > maxImageBytes {
			return db.Attachment{}, fmt.Errorf("image %q has %d bytes; at most %d are accepted", name, size, maxImageBytes)
		}
		att.Kind, att.File = db.AttachmentImage, att.ID+ext
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return db.Attachment{}, err
		}
		att.Width, att.Height = imageSize(src, head)
		att.Tokens = imageTokens(att.Width, att.Height)
		return att, nil
	}
	notText := fmt.Errorf("%q (%s) is neither an image (PNG, JPEG, GIF, WebP) nor a text file", name, mime)
	if bytes.IndexByte(head, 0) >= 0 {
		return db.Attachment{}, notText
	}
	if size > maxTextFileBytes {
		return db.Attachment{}, fmt.Errorf("text file %q has %d bytes; at most %d are accepted", name, size, maxTextFileBytes)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return db.Attachment{}, err
	}
	data, err := io.ReadAll(io.LimitReader(src, maxTextFileBytes+1))
	if err != nil {
		return db.Attachment{}, fmt.Errorf("attachment %q cannot be read: %w", name, err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return db.Attachment{}, notText
	}
	if !strings.HasPrefix(mime, "text/") && mime != "application/json" {
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

// imageSize reads an image's dimensions from its header; head, its first bytes, is enough for WebP.
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

// imageTokens estimates what a provider charges for an image scaled to fit maxImageSide: about one token per 750 pixels.
func imageTokens(w, h int) int {
	if w <= 0 || h <= 0 {
		return defaultImageToken
	}
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

func (sv *Service) removeAttachments(atts []db.Attachment) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(sv.ctx), attachmentTimeout)
	defer cancel()
	for _, att := range atts {
		if err := sv.cfg.Attachments.Delete(ctx, att.File); err != nil {
			slog.Warn("attachment not removed", "file", att.File, "err", err)
		}
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
