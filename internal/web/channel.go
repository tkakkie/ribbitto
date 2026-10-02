package web

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// ChannelService provides channels within the resolved member's organisation.
type ChannelService interface {
	List(context.Context, authz.Membership) ([]domain.Channel, error)
	Get(context.Context, authz.Membership, domain.ID) (domain.Channel, error)
	Default(context.Context, authz.Membership) (domain.Channel, error)
	Create(context.Context, authz.Membership, string) (domain.Channel, error)
}

// MessageReader provides the channel page and cursor from one snapshot, and
// one message by its event sequence for the stream.
type MessageReader interface {
	Page(context.Context, authz.Membership, domain.ID, *domain.ID, *int64) (message.ChannelPage, error)
	One(context.Context, authz.Membership, domain.ID, int64) (message.Entry, error)
}

// TopicReader looks up topics scoped to their organisation and channel.
type TopicReader interface {
	GetTopic(context.Context, domain.ID, domain.ID, domain.ID) (domain.Topic, error)
}

type channelPages struct {
	topics   TopicReader
	topicID  *domain.ID // Set only on the request-local copy in show.
	stream   *Streaming
	renders  *realtime.Cache[renderKey, realtime.Outgoing]
	messages MessageReader
	posting  *message.Service
	pages    *pageRenderer
	service  ChannelService
}

func (p channelPages) home(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	c, err := p.service.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
}

func (p channelPages) show(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if raw := r.PathValue("topicID"); raw != "" {
		selected, ok := pathID(raw)
		if !ok {
			http.NotFound(w, r)
			return
		}
		p.topicID = &selected
	}
	if r.Method == http.MethodPost {
		p.post(w, r, m, id)
		return
	}
	page := view.ChannelPage{}
	// ParseQuery, unlike URL.Query, reports malformed pairs instead of
	// dropping them, so a broken link cannot fall back to the latest page.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		p.invalidQuery(w, r, m, id)
		return
	}
	if raw, ok := query["before"]; ok {
		// An event_seq is positive; anything else is a malformed link.
		before, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || before < 1 {
			p.invalidQuery(w, r, m, id)
			return
		}
		page.Before = before
	}
	p.render(w, r, m, id, http.StatusOK, page)
}

// Preserve channel lookup errors ahead of malformed paging links, without a
// separate channel read on successfully rendered pages.
func (p channelPages) invalidQuery(w http.ResponseWriter, r *http.Request, m authz.Membership, id domain.ID) {
	_, err := p.service.Get(r.Context(), m, id)
	if err == nil && p.topicID != nil {
		_, err = p.topics.GetTopic(r.Context(), m.Organization.ID, id, *p.topicID)
	}
	switch {
	case errors.Is(err, channel.ErrNotFound), errors.Is(err, topic.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "finding channel", err)
	default:
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}
}

func (p channelPages) create(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	if !parseForm(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	c, err := p.service.Create(r.Context(), m, name)
	message := ""
	switch {
	case err == nil:
		http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
		return
	case errors.Is(err, channel.ErrInvalidName):
		message = "channel.error.name"
	case errors.Is(err, channel.ErrNameTaken):
		message = "channel.error.name_taken"
	default:
		serverError(w, r, "creating channel", err)
		return
	}
	// No channel identity is accepted from the form: failed creation returns
	// to the default conversation within the resolved organisation.
	c, err = p.service.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	p.render(w, r, m, c.ID, http.StatusUnprocessableEntity, view.ChannelPage{Name: name, Error: message})
}

func (p channelPages) render(w http.ResponseWriter, r *http.Request, m authz.Membership, id domain.ID, status int, page view.ChannelPage) {
	var before *int64
	if page.Before > 0 {
		before = &page.Before
	}
	history, err := p.messages.Page(r.Context(), m, id, p.topicID, before)
	if errors.Is(err, channel.ErrNotFound) || errors.Is(err, authz.ErrNotFound) || errors.Is(err, topic.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "reading channel page", err)
		return
	}
	account, _ := middleware.Account(r.Context())
	page.Messages = make([]view.Message, len(history.Entries))
	for i, entry := range history.Entries {
		page.Messages[i] = viewMessage(m.Organization.Slug, entry)
	}
	p.pages.render(w, r, status, func(url string) templ.Component {
		page.Organization, page.DisplayName, page.Handle, page.Role = m.Organization, account.DisplayName, m.Member.Handle, string(m.Member.Role)
		page.Current, page.Channels, page.Older = history.Current, history.Channels, history.Older
		page.EventCursor, page.Topic, page.Topics = history.EventCursor, history.Topic, history.Topics
		return view.Channel(url, page)
	})
}

func (p channelPages) post(w http.ResponseWriter, r *http.Request, m authz.Membership, id domain.ID) {
	c, err := p.service.Get(r.Context(), m, id)
	if errors.Is(err, channel.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "finding channel", err)
		return
	}
	if !parseForm(w, r) {
		return
	}
	body := r.PostForm.Get("body")
	posted, err := p.posting.PostToTopic(r.Context(), m, c.ID, p.topicID, body)
	switch {
	case errors.Is(err, message.ErrInvalidBody):
		p.renderComposer(w, r, m, c, http.StatusUnprocessableEntity, view.ChannelPage{Body: body, BodyError: "message.error.body"})
	case errors.Is(err, channel.ErrNotFound), errors.Is(err, authz.ErrNotFound), errors.Is(err, topic.ErrNotFound):
		// The channel, membership or organisation went away after this
		// request resolved them; answer as for a non-member.
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "posting message", err)
	case r.Header.Get("HX-Request") == "true" && p.topicID == nil:
		p.renderComposer(w, r, m, c, http.StatusOK, view.ChannelPage{PostedMessageID: &posted.ID})
	default:
		http.Redirect(w, r, view.ConversationURL(m.Organization.Slug, c.ID, p.topicID), http.StatusSeeOther)
	}
}

// Enhanced posts never replace history or the connection's snapshot cursor.
func (p channelPages) renderComposer(w http.ResponseWriter, r *http.Request, m authz.Membership, c domain.Channel, status int, page view.ChannelPage) {
	if r.Header.Get("HX-Request") == "true" && p.topicID == nil {
		page.Organization, page.Current = m.Organization, c
		templ.Handler(view.MessageComposer(page), templ.WithStatus(status)).ServeHTTP(w, r)
		return
	}
	p.render(w, r, m, c.ID, status, page)
}

// channelID parses the {channelID} path segment: the UUID in its canonical
// form, the only one ChannelURL produces.
func channelID(r *http.Request) (domain.ID, bool) {
	return pathID(r.PathValue("channelID"))
}

func pathID(raw string) (domain.ID, bool) {
	var id domain.ID
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return id, false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(raw, "-", ""))
	if err != nil || len(decoded) != len(id) {
		return id, false
	}
	copy(id[:], decoded)
	return id, true
}

// viewMessage converts a history entry into what MessageItem renders, for
// the page and the stream alike; slug is the organisation's, for the label's
// link to the topic view.
func viewMessage(slug string, entry message.Entry) view.Message {
	return view.Message{
		ID: entry.ID, DisplayName: entry.DisplayName, Handle: entry.Handle,
		CreatedAt: entry.CreatedAt, Body: entry.Body, EventSeq: entry.EventSeq,
		TopicName: entry.TopicName, DefaultTopic: entry.DefaultTopic,
		TopicURL: view.ConversationURL(slug, entry.ChannelID, &entry.TopicID),
	}
}
