package web

import (
	"crypto/sha256"
	"errors"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func channelImageURL(id kernel.ID, key string) string {
	if key != "" && conversation.ValidateChannelImage(key) == nil {
		return view.ChannelImageURL(key)
	}
	// Hash all UUID bytes, including the random suffix, rather than grouping
	// channels created close together by their UUIDv7 timestamp.
	keys := conversation.ChannelImageKeys()
	digest := sha256.Sum256(id[:])
	return view.ChannelImageURL(keys[int(digest[0])%len(keys)])
}

func (p channelPages) changeImage(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, ok := p.resolvePlace(w, r, m, id, r.PathValue("topicID")); !ok {
		return
	}
	// The redirect returns to the page's own position; a malformed one
	// changes nothing, like the page it came from would refuse it.
	_, at, ok := conversationQuery(r.URL.RawQuery)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if !parseForm(w, r) {
		return
	}
	err := p.channels.SetImage(r.Context(), m, id, r.PostForm.Get("image"))
	switch {
	case errors.Is(err, conversation.ErrInvalidChannelImage):
		p.overviewImageErr = "illustration.error"
		request := r.Clone(r.Context())
		request.Method = http.MethodGet
		p.overview(w, request, m)
	case errors.Is(err, conversation.ErrChannelNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "changing channel image", err)
	default:
		target := r.URL.EscapedPath()
		if state := conversationState(at); state != "" {
			target += "?" + state
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}
