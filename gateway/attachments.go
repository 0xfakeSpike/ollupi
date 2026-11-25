package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/store"
)

const AttachmentPath = "/attachments/{thread}/{attachment}"

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r)
	if !ok || !s.serves(w, r, id.User) {
		return
	}
	att, data, err := s.app.ReadAttachment(r.Context(), id.User, r.PathValue("thread"), r.PathValue("attachment"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "no such attachment", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "the attachment could not be read now", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", att.MimeType)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": att.Name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(data)
}

func (s *Server) allowsOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	for _, pattern := range s.origins {
		target := u.Host
		if strings.Contains(pattern, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

// UploadPath takes a file a person sends from their device, before the
// message that sends it: the body is the file, Content-Disposition names
// it (filename or filename*), and Content-Type is its type, which ollupi
// checks against the content. It answers an UploadResult.
const UploadPath = "/uploads"

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r)
	if !ok || !s.serves(w, r, id.User) {
		return
	}
	name := ""
	if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, app.MaxUploadBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		http.Error(w, fmt.Sprintf("the file has more than %d bytes", app.MaxUploadBytes), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		http.Error(w, "the file was not received whole", http.StatusBadRequest)
		return
	}
	att, err := s.app.Upload(r.Context(), id.User, name, kind, data)
	switch {
	case errors.Is(err, app.ErrInvalidMessage):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, "the file could not be kept now", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(UploadResult{UploadID: att.ID, Name: att.Name, MimeType: att.MimeType, Size: att.Size, Kind: att.Kind})
}
