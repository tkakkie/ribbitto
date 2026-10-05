package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Branching moves selected messages to another topic of the channel.
type Branching interface {
	Branch(ctx context.Context, m org.Membership, channelID domain.ID, b conversation.Branch, notice func(conversation.Topic) string) (conversation.Topic, error)
}

// branch serves POST …/channels/{channelID}/branch. The form names the
// messages (message, repeated), the topic they are expected in (from), and
// the destination: an existing topic (to) or a new one (name). Success is
// 303 to the destination's topic view (HX-Redirect for enhanced posts).
func (p channelPages) branch(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := channelID(r)
	if !ok || p.branching == nil {
		http.NotFound(w, r)
		return
	}
	if !parseForm(w, r) {
		return
	}
	// Each selectable item carries its rendered source, including without JS.
	from := r.PostForm.Get("from")
	for i, raw := range r.PostForm["message"] {
		if message, source, paired := strings.Cut(raw, "/"); paired {
			if from != "" && from != source {
				p.branchError(w, r, m, id, 422, "topic.branch_mixed")
				return
			}
			from = source
			r.PostForm["message"][i] = message
		}
	}
	var b conversation.Branch
	for _, raw := range r.PostForm["message"] {
		message, ok := pathID(raw)
		if !ok {
			p.branchError(w, r, m, id, 422, "topic.branch_invalid")
			return
		}
		b.Messages = append(b.Messages, message)
	}
	if b.From, ok = pathID(from); !ok {
		p.branchError(w, r, m, id, 422, "topic.branch_invalid")
		return
	}
	if raw := r.PostForm.Get("to"); raw != "" {
		to, ok := pathID(raw)
		if !ok {
			p.branchError(w, r, m, id, 422, "topic.branch_invalid")
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
	notice := func(destination conversation.Topic) string {
		name := destination.Name
		if destination.IsDefault {
			name = i18n.T(r.Context(), "topic.default")
		}
		return strings.NewReplacer("{topic}", name, "{count}", strconv.Itoa(count)).Replace(i18n.T(r.Context(), "topic.branch_notice"))
	}
	destination, err := p.branching.Branch(r.Context(), m, id, b, notice)
	switch {
	case errors.Is(err, conversation.ErrBranchConflict):
		p.branchError(w, r, m, id, 409, "topic.branch_conflict")
	case errors.Is(err, conversation.ErrInvalidTopicName):
		p.branchError(w, r, m, id, 422, "topic.branch_name_invalid")
	case errors.Is(err, conversation.ErrTopicNameTaken):
		p.branchError(w, r, m, id, 422, "topic.branch_name_taken")
	case errors.Is(err, conversation.ErrInvalidBranch):
		p.branchError(w, r, m, id, 422, "topic.branch_invalid")
	case errors.Is(err, conversation.ErrTopicNotFound), errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, org.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "branching messages", err)
	default:
		url := view.ConversationURL(m.Organization.Slug, id, &destination.ID)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", url)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, url, http.StatusSeeOther)
	}
}

func (p channelPages) branchError(w http.ResponseWriter, r *http.Request, m org.Membership, id domain.ID, status int, key string) {
	// Scope error responses too; invalid input must not reveal another channel.
	if _, err := p.service.Get(r.Context(), m, id); err != nil {
		if errors.Is(err, conversation.ErrChannelNotFound) {
			http.NotFound(w, r)
		} else {
			serverError(w, r, "finding channel", err)
		}
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		templ.Handler(view.BranchFeedback(key), templ.WithStatus(status)).ServeHTTP(w, r)
		return
	}
	if selected, ok := pathID(r.PostForm.Get("return_topic")); ok {
		p.topicID = &selected
	}
	before, _ := strconv.ParseInt(r.PostForm.Get("before"), 10, 64)
	p.render(w, r, m, id, status, view.ChannelPage{Before: before, BranchError: key, BranchName: r.PostForm.Get("name")})
}
