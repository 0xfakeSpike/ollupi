package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

const (
	maxAttachments    = 10
	maxImageBytes     = 20 << 20
	maxTextFileBytes  = 256 << 10
	maxImageSide      = 1568
	minImageTokens    = 85
	defaultImageToken = 1600
)

// AttachmentInput is a file on this machine sent with a message; the attachment is a copy taken when it is sent.
type AttachmentInput struct {
	Path     string
	Name     string
	MimeType string
}

var imageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func (a *Agent) storeAttachments(threadID string, ins []AttachmentInput) ([]repo.Attachment, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if a.cfg.AttachmentsDir == "" {
		return nil, fmt.Errorf("agent: %w: this agent takes no attachments", ErrInvalidMessage)
	}
	if len(ins) > maxAttachments {
		return nil, fmt.Errorf("agent: %w: %d attachments; at most %d are accepted", ErrInvalidMessage, len(ins), maxAttachments)
	}
	out := make([]repo.Attachment, 0, len(ins))
	for _, in := range ins {
		att, err := a.storeAttachment(threadID, in)
		if err != nil {
			a.removeAttachments(out)
			return nil, err
		}
		out = append(out, att)
	}
	return out, nil
}

func (a *Agent) storeAttachment(threadID string, in AttachmentInput) (repo.Attachment, error) {
	if !filepath.IsAbs(in.Path) {
		return repo.Attachment{}, fmt.Errorf("agent: %w: attachment path %q is not absolute", ErrInvalidMessage, in.Path)
	}
	src, err := os.Open(in.Path)
	if err != nil {
		return repo.Attachment{}, fmt.Errorf("agent: %w: attachment %s cannot be read: %v", ErrInvalidMessage, in.Path, err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return repo.Attachment{}, fmt.Errorf("agent: %w: attachment %s is not a file", ErrInvalidMessage, in.Path)
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = in.Path
	}
	att, err := describe(src, info.Size(), attachmentName(name), in.MimeType)
	if err != nil {
		return repo.Attachment{}, fmt.Errorf("agent: %w: %v", ErrInvalidMessage, err)
	}
	att.File = threadID + "/" + att.File
	if err := a.copyAttachment(src, att); err != nil {
		return repo.Attachment{}, fmt.Errorf("agent: store attachment %s: %w", att.Name, err)
	}
	return att, nil
}

// copyAttachment copies exactly the bytes describe measured, so a file that changes meanwhile is refused.
func (a *Agent) copyAttachment(src io.ReadSeeker, att repo.Attachment) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	path := filepath.Join(a.cfg.AttachmentsDir, filepath.FromSlash(att.File))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, io.LimitReader(src, att.Size+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != att.Size {
		err = fmt.Errorf("the file changed while it was being attached")
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// describe classifies a file as an image or a text file from its first bytes and checks it against the limits
// of its kind before reading more of it.
func describe(src io.ReadSeeker, size int64, name, mime string) (repo.Attachment, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return repo.Attachment{}, fmt.Errorf("attachment %q cannot be read: %v", name, err)
	}
	head = head[:n]
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "" {
		mime, _, _ = strings.Cut(http.DetectContentType(head), ";")
	}
	att := repo.Attachment{ID: uuid.NewString(), Name: name, MimeType: mime, Size: size}
	if size == 0 {
		return repo.Attachment{}, fmt.Errorf("attachment %q is empty", name)
	}
	if ext, ok := imageTypes[mime]; ok {
		if size > maxImageBytes {
			return repo.Attachment{}, fmt.Errorf("image %q has %d bytes; at most %d are accepted", name, size, maxImageBytes)
		}
		att.Kind, att.File = repo.AttachmentImage, att.ID+ext
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return repo.Attachment{}, err
		}
		att.Width, att.Height = imageSize(src, head)
		att.Tokens = imageTokens(att.Width, att.Height)
		return att, nil
	}
	notText := fmt.Errorf("%q (%s) is neither an image (PNG, JPEG, GIF, WebP) nor a text file", name, mime)
	if bytes.IndexByte(head, 0) >= 0 {
		return repo.Attachment{}, notText
	}
	if size > maxTextFileBytes {
		return repo.Attachment{}, fmt.Errorf("text file %q has %d bytes; at most %d are accepted", name, size, maxTextFileBytes)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return repo.Attachment{}, err
	}
	data, err := io.ReadAll(io.LimitReader(src, maxTextFileBytes+1))
	if err != nil {
		return repo.Attachment{}, fmt.Errorf("attachment %q cannot be read: %v", name, err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return repo.Attachment{}, notText
	}
	if !strings.HasPrefix(mime, "text/") && mime != "application/json" {
		att.MimeType = "text/plain"
	}
	att.Kind, att.File = repo.AttachmentFile, att.ID+".txt"
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

func (a *Agent) removeAttachments(atts []repo.Attachment) {
	for _, att := range atts {
		os.Remove(filepath.Join(a.cfg.AttachmentsDir, filepath.FromSlash(att.File)))
	}
}

func (a *Agent) ReadAttachment(threadID, id string) (repo.Attachment, []byte, error) {
	att, err := a.cfg.DB.Attachment(threadID, id)
	if err != nil {
		return repo.Attachment{}, nil, fmt.Errorf("agent: attachment %s: %w", id, err)
	}
	data, err := os.ReadFile(filepath.Join(a.cfg.AttachmentsDir, filepath.FromSlash(att.File)))
	if err != nil {
		return repo.Attachment{}, nil, fmt.Errorf("agent: attachment %s: %w", id, err)
	}
	return att, data, nil
}

func (a *Agent) checkImages(threadID string, model llm.Model, atts []repo.Attachment) error {
	if model.Images {
		return nil
	}
	for _, att := range atts {
		if att.Kind == repo.AttachmentImage {
			return fmt.Errorf("agent: %w: model %s does not accept images; pick a model that does", ErrInvalidMessage, model.Name)
		}
	}
	has, err := a.cfg.DB.HasImages(threadID)
	if err != nil {
		return fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if has {
		return fmt.Errorf("agent: %w: this conversation shows the model images, and model %s does not accept them; pick a model that does", ErrInvalidMessage, model.Name)
	}
	return nil
}
