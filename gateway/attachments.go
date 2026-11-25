package gateway

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

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
