package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Branching moves selected messages to another topic of the channel.
type Branching interface {
	Branch(ctx context.Context, m authz.Membership, channelID domain.ID, b topic.Branch, notice func(domain.Topic) string) (domain.Topic, error)
}

// branch serves POST …/channels/{channelID}/branch. The form names the
// messages (message, repeated), the topic they are expected in (from), and
// the destination: an existing topic (to) or a new one (name). Success is
// 303 to the destination's topic view. The selection UI is #308's; until
// then failures answer with their status and a plain message.
func (p channelPages) branch(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	id, ok := channelID(r)
	if !ok || p.branching == nil {
		http.NotFound(w, r)
		return
	}
	if !parseForm(w, r) {
		return
	}
	var b topic.Branch
	for _, raw := range r.PostForm["message"] {
		message, ok := pathID(raw)
		if !ok {
			http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
			return
		}
		b.Messages = append(b.Messages, message)
	}
	if b.From, ok = pathID(r.PostForm.Get("from")); !ok {
		http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
		return
	}
	if raw := r.PostForm.Get("to"); raw != "" {
		to, ok := pathID(raw)
		if !ok {
			http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
			return
		}
		b.To = &to
	}
	// Read both: a request naming two destinations is refused (422), never
	// resolved by dropping one.
	b.NewName = r.PostForm.Get("name")
	count := len(b.Messages)
	// The notice is an ordinary message in the brancher's language; its
	// text names the destination as it is at the time of branching.
	notice := func(destination domain.Topic) string {
		name := destination.Name
		if destination.IsDefault {
			name = i18n.T(r.Context(), "topic.default")
		}
		return strings.NewReplacer("{topic}", name, "{count}", strconv.Itoa(count)).Replace(i18n.T(r.Context(), "topic.branch_notice"))
	}
	destination, err := p.branching.Branch(r.Context(), m, id, b, notice)
	switch {
	case errors.Is(err, topic.ErrConflict):
		http.Error(w, "Conflict", http.StatusConflict)
	case errors.Is(err, topic.ErrInvalidBranch), errors.Is(err, topic.ErrInvalidName), errors.Is(err, topic.ErrNameTaken):
		http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
	case errors.Is(err, topic.ErrNotFound), errors.Is(err, channel.ErrNotFound), errors.Is(err, authz.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "branching messages", err)
	default:
		http.Redirect(w, r, view.ConversationURL(m.Organization.Slug, id, &destination.ID), http.StatusSeeOther)
	}
}
