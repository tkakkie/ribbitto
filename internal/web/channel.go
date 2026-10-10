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
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Channels provides channels within the resolved member's organisation.
type Channels interface {
	Get(context.Context, org.Membership, kernel.ID) (conversation.Channel, error)
	Default(context.Context, org.Membership) (conversation.Channel, error)
	Create(context.Context, org.Membership, string) (conversation.Channel, error)
}

// MessageReader provides the channel page and cursor from one snapshot, and
// one message by sequence or a bounded batch by ID for the stream.
type MessageReader interface {
	Members(context.Context, org.Membership, kernel.ID, *kernel.ID) (conversation.MembersPage, error)
	Many(context.Context, org.Membership, kernel.ID, []kernel.ID) ([]conversation.Entry, error)
	Page(context.Context, org.Membership, kernel.ID, *kernel.ID, *int64) (conversation.ChannelPage, error)
	One(context.Context, org.Membership, kernel.ID, int64) (conversation.Entry, error)
}

// TopicLookup looks up a topic of a channel in the resolved member's
// organisation; conversation's root owns that scope check.
type TopicLookup interface {
	Get(context.Context, org.Membership, kernel.ID, kernel.ID) (conversation.Topic, error)
}

type channelPages struct {
	branching Branching
	topics    TopicLookup
	topicID   *kernel.ID // Set only on the request-local copy in show.
	stream    *Streaming
	renders   *realtime.Cache[renderKey, realtime.Outgoing]
	messages  MessageReader
	posting   *conversation.Posting
	pages     *pageRenderer
	channels  Channels
}

func (p channelPages) home(w http.ResponseWriter, r *http.Request, m org.Membership) {
	c, err := p.channels.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
}

func (p channelPages) show(w http.ResponseWriter, r *http.Request, m org.Membership) {
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
func (p channelPages) invalidQuery(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID) {
	_, err := p.channels.Get(r.Context(), m, id)
	if err == nil && p.topicID != nil {
		_, err = p.topics.Get(r.Context(), m, id, *p.topicID)
	}
	switch {
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, conversation.ErrTopicNotFound):
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "finding channel", err)
	default:
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}
}

func (p channelPages) create(w http.ResponseWriter, r *http.Request, m org.Membership) {
	if !parseForm(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	c, err := p.channels.Create(r.Context(), m, name)
	message := ""
	switch {
	case err == nil:
		http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
		return
	case errors.Is(err, conversation.ErrInvalidChannelName):
		message = "channel.error.name"
	case errors.Is(err, conversation.ErrChannelNameTaken):
		message = "channel.error.name_taken"
	default:
		serverError(w, r, "creating channel", err)
		return
	}
	// No channel identity is accepted from the form: failed creation returns
	// to the default conversation within the resolved organisation.
	c, err = p.channels.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	p.render(w, r, m, c.ID, http.StatusUnprocessableEntity, view.ChannelPage{Name: name, Error: message})
}

func (p channelPages) render(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID, status int, page view.ChannelPage) {
	var before *int64
	if page.Before > 0 {
		before = &page.Before
	}
	history, err := p.messages.Page(r.Context(), m, id, p.topicID, before)
	if errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) || errors.Is(err, conversation.ErrTopicNotFound) {
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
		page.Organization, page.DisplayName, page.Handle, page.Role = view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name}, memberDisplayName(account.DisplayName), m.Member.Handle, string(m.Member.Role)
		page.Current, page.Channels, page.Older = viewChannel(history.Current), viewChannels(history.Channels), history.Older
		page.EventCursor, page.Topic, page.Topics = history.EventCursor, viewTopicPtr(history.Topic), viewTopics(history.Topics)
		return view.ChannelScreen(url, page)
	})
}

func (p channelPages) post(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID) {
	c, err := p.channels.Get(r.Context(), m, id)
	if errors.Is(err, conversation.ErrChannelNotFound) {
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
	case errors.Is(err, conversation.ErrInvalidBody):
		p.renderComposer(w, r, m, c, http.StatusUnprocessableEntity, view.ChannelPage{Body: body, BodyError: "message.error.body"})
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, org.ErrNotFound), errors.Is(err, conversation.ErrTopicNotFound):
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
func (p channelPages) renderComposer(w http.ResponseWriter, r *http.Request, m org.Membership, c conversation.Channel, status int, page view.ChannelPage) {
	if r.Header.Get("HX-Request") == "true" && p.topicID == nil {
		page.Organization, page.Current = view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name}, viewChannel(c)
		templ.Handler(view.MessageComposer(page), templ.WithStatus(status)).ServeHTTP(w, r)
		return
	}
	p.render(w, r, m, c.ID, status, page)
}

// channelID parses the {channelID} path segment: the UUID in its canonical
// form, the only one ChannelURL produces.
func channelID(r *http.Request) (kernel.ID, bool) {
	return pathID(r.PathValue("channelID"))
}

func pathID(raw string) (kernel.ID, bool) {
	var id kernel.ID
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

// viewChannel and the helpers below copy only what the views display, so
// view never depends on the use cases' entities.
func viewChannel(c conversation.Channel) view.Channel {
	return view.Channel{ID: c.ID, Name: c.Name}
}

func viewChannels(channels []conversation.Channel) []view.Channel {
	out := make([]view.Channel, len(channels))
	for i, c := range channels {
		out[i] = viewChannel(c)
	}
	return out
}

func viewTopic(t conversation.Topic) view.Topic {
	return view.Topic{ID: t.ID, Name: t.Name, IsDefault: t.IsDefault}
}

func viewTopicPtr(t *conversation.Topic) *view.Topic {
	if t == nil {
		return nil
	}
	v := viewTopic(*t)
	return &v
}

func viewTopics(topics []conversation.Topic) []view.Topic {
	out := make([]view.Topic, len(topics))
	for i, t := range topics {
		out[i] = viewTopic(t)
	}
	return out
}

// viewMessage converts a history entry into what MessageItem renders, for
// the page and the stream alike; slug is the organisation's, for the label's
// link to the topic view.
func viewMessage(slug string, entry conversation.Entry) view.Message {
	return view.Message{
		ID: entry.ID, DisplayName: memberDisplayName(entry.DisplayName), Handle: entry.Handle,
		CreatedAt: entry.CreatedAt, Body: entry.Body, EventSeq: entry.EventSeq,
		TopicID: entry.TopicID, TopicName: entry.TopicName, DefaultTopic: entry.DefaultTopic,
		TopicURL: view.ConversationURL(slug, entry.ChannelID, &entry.TopicID),
	}
}

// memberDisplayName keeps legacy blank-looking names out of the view, where
// an empty name means to show only the handle.
func memberDisplayName(name string) string {
	if identity.IsBlankLookingName(name) {
		return ""
	}
	return name
}
