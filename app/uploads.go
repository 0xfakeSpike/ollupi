package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
)

// A file a person sends from their own device reaches ollupi before the
// message that sends it: Upload keeps it, checked as an attachment, and a
// message names it by its id (AttachmentInput.Upload), which takes it into
// the conversation. An upload is its user's alone, sent once, and dropped
// when no message takes it within uploadTTL, or when the deployment
// restarts.
const (
	uploadTTL         = time.Hour
	maxPendingUploads = 4 * maxAttachments
	uploadsPrefix     = "uploads/"
)

type upload struct {
	user string
	att  conversation.Attachment
	at   time.Time
}

type uploads struct {
	mu      sync.Mutex
	pending map[string]upload
}

// MaxUploadBytes is the most an upload holds.
const MaxUploadBytes = maxImageBytes

func uploadKey(user, id string) string { return uploadsPrefix + user + "/" + id }

// ClearUploads drops the uploads an earlier run of the deployment kept,
// which no message can name any more.
func (a *App) ClearUploads(ctx context.Context) error {
	return a.attachments.DeletePrefix(ctx, uploadsPrefix)
}

// Upload keeps a file a person sends from their device, under name, for a
// message to take. It is checked as any attachment is: an image or a text
// file within the limits.
func (a *App) Upload(ctx context.Context, user, name, mime string, data []byte) (conversation.Attachment, error) {
	if len(data) > MaxUploadBytes {
		return conversation.Attachment{}, fmt.Errorf("%w: %d bytes; at most %d are accepted", ErrInvalidMessage, len(data), MaxUploadBytes)
	}
	att, err := describe(data, attachmentName(name), mime)
	if err != nil {
		return conversation.Attachment{}, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	now := time.Now()
	a.dropExpiredUploads(ctx, now)
	a.uploads.mu.Lock()
	held := 0
	for _, u := range a.uploads.pending {
		if u.user == user {
			held++
		}
	}
	a.uploads.mu.Unlock()
	if held >= maxPendingUploads {
		return conversation.Attachment{}, fmt.Errorf("%w: %d files wait to be sent already; send them first", ErrInvalidMessage, held)
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	if err := a.attachments.Put(ctx, uploadKey(user, att.ID), data); err != nil {
		return conversation.Attachment{}, fmt.Errorf("keep upload %s: %w", att.Name, err)
	}
	a.uploads.mu.Lock()
	if a.uploads.pending == nil {
		a.uploads.pending = map[string]upload{}
	}
	a.uploads.pending[att.ID] = upload{user: user, att: att, at: now}
	a.uploads.mu.Unlock()
	return att, nil
}

// takeUpload is an upload of user's with its content, which no message
// can take again.
func (a *App) takeUpload(ctx context.Context, user, id string) (conversation.Attachment, []byte, error) {
	a.uploads.mu.Lock()
	u, ok := a.uploads.pending[id]
	if ok && u.user == user && time.Since(u.at) <= uploadTTL {
		delete(a.uploads.pending, id)
	} else {
		ok = false
	}
	a.uploads.mu.Unlock()
	if !ok {
		return conversation.Attachment{}, nil, fmt.Errorf("%w: no upload %s waits to be sent; upload the file again", ErrInvalidMessage, id)
	}
	data, err := a.attachments.Get(ctx, uploadKey(user, id))
	if err != nil {
		return conversation.Attachment{}, nil, fmt.Errorf("upload %s: %w", u.att.Name, err)
	}
	if err := a.attachments.Delete(ctx, uploadKey(user, id)); err != nil {
		slog.Warn("app: an upload taken into a conversation is left in the store", "user", user, "upload", id, "error", err)
	}
	return u.att, data, nil
}

func (a *App) dropExpiredUploads(ctx context.Context, now time.Time) {
	a.uploads.mu.Lock()
	var expired []upload
	for _, id := range slices.Collect(maps.Keys(a.uploads.pending)) {
		if u := a.uploads.pending[id]; now.Sub(u.at) > uploadTTL {
			expired = append(expired, u)
			delete(a.uploads.pending, id)
		}
	}
	a.uploads.mu.Unlock()
	for _, u := range expired {
		if err := a.attachments.Delete(ctx, uploadKey(u.user, u.att.ID)); err != nil {
			slog.Warn("app: an expired upload is left in the store", "user", u.user, "upload", u.att.ID, "error", err)
		}
	}
}
