package web

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Channels provides channels within the resolved member's organisation.
type Channels interface {
	SetImage(context.Context, org.Membership, kernel.ID, string) error
	Get(context.Context, org.Membership, kernel.ID) (conversation.Channel, error)
	Default(context.Context, org.Membership) (conversation.Channel, error)
	CreateWithImage(context.Context, org.Membership, string, string) (conversation.Channel, error)
}

// MessageReader provides the channel page and cursor from one snapshot, with
// the channel's pins when the overview is open, the sidebar snapshot the channel list page shows, one message by sequence or
// a bounded batch by ID for the stream, and where a message is now for its URL.
type MessageReader interface {
	ReadPins(context.Context, org.Membership, kernel.ID) (conversation.PinsSnapshot, error)
	Around(context.Context, org.Membership, kernel.ID, *kernel.ID, int64) (conversation.ChannelPage, error)
	Locate(context.Context, org.Membership, kernel.ID) (conversation.Location, error)
	Members(context.Context, org.Membership, kernel.ID, *kernel.ID) (conversation.MembersPage, error)
	Overview(context.Context, org.Membership, kernel.ID, *kernel.ID, *int64, int64) (conversation.ChannelPage, error)
	Sidebar(context.Context, org.Membership, kernel.ID, *kernel.ID) (conversation.SidebarSnapshot, error)
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
	typing           *typing.State
	limiter          *middleware.MemberRateLimiter
	createLimit      *middleware.MemberRateLimiter
	branchLimit      *middleware.MemberRateLimiter
	readLimit        *middleware.MemberRateLimiter
	reading          Reading
	branching        Branching
	pinning          Pinning
	topics           TopicLookup
	topicID          *kernel.ID // Set only on the request-local copy in show.
	panel            string     // Set only on the request-local copy by a panel route.
	after            *kernel.ID // The members panel's cursor, set only in show.
	stream           *Streaming
	renders          *realtime.Cache[renderKey, realtime.Outgoing]
	messages         MessageReader
	posting          *conversation.Posting
	pages            *pageRenderer
	channels         Channels
	overviewImageErr string // Set only on the request-local copy after a failed image change.
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
	page := view.ChannelPage{Panel: p.panel, OverviewImageError: p.overviewImageErr}
	query, at, ok := conversationQuery(r.URL.RawQuery)
	if !ok {
		p.invalidQuery(w, r, m, id)
		return
	}
	page.Before, page.Around = at.before, at.around
	// The members' cursor stays apart from the conversation's position.
	if raw, ok := query["after"]; ok && p.panel == view.PanelMembers {
		after, valid := pathID(raw[0])
		if len(raw) != 1 || !valid {
			p.invalidQuery(w, r, m, id)
			return
		}
		p.after = &after
	}
	status := http.StatusOK
	if p.overviewImageErr != "" {
		status = http.StatusUnprocessableEntity
	}
	p.render(w, r, m, id, status, page, at.firstUnread)
}

// position is where in a conversation a page is: the latest page, or an
// older page before an event_seq or around one (decision 36).
type position struct {
	before, around int64
	firstUnread    *int64
}

// conversationQuery parses a page's own parameters. ParseQuery, unlike
// URL.Query, reports malformed pairs instead of dropping them, so a broken
// link cannot fall back to the latest page.
func conversationQuery(raw string) (query url.Values, at position, ok bool) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, position{}, false
	}
	for key, dest := range map[string]*int64{"before": &at.before, "around": &at.around} {
		if raw, ok := query[key]; ok {
			// An event_seq is positive; anything else is a malformed link.
			parsed, err := strconv.ParseInt(raw[0], 10, 64)
			if len(raw) != 1 || err != nil || parsed < 1 {
				return nil, position{}, false
			}
			*dest = parsed
		}
	}
	if at.before > 0 && at.around > 0 {
		return nil, position{}, false
	}
	// An older-history link carries the visit's divider so its read POSTs cannot move it.
	if raw, ok := query["unread"]; ok {
		seq, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || seq < 0 || !at.older() {
			return nil, position{}, false
		}
		at.firstUnread = &seq
	}
	return query, at, true
}

func (at position) older() bool { return at.before > 0 || at.around > 0 }

// conversationState encodes the conversation position a page renders, so
// panel links and redirects carry exactly that, never a request's other keys.
// The divider belongs to an older page only, like the older link's.
func conversationState(at position) string {
	state := url.Values{}
	switch {
	case at.before > 0:
		state.Set("before", strconv.FormatInt(at.before, 10))
	case at.around > 0:
		state.Set("around", strconv.FormatInt(at.around, 10))
	}
	if at.older() && at.firstUnread != nil {
		state.Set("unread", strconv.FormatInt(*at.firstUnread, 10))
	}
	return state.Encode()
}

// Preserve channel lookup errors ahead of malformed paging links, without a
// separate channel read on successfully rendered pages.
func (p channelPages) invalidQuery(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID) {
	if _, ok := p.resolvePlace(w, r, m, id, r.PathValue("topicID")); !ok {
		return
	}
	http.Error(w, "Bad Request", http.StatusBadRequest)
}

func (p channelPages) create(w http.ResponseWriter, r *http.Request, m org.Membership) {
	if !p.createLimit.Allow(m.Organization.ID, m.Member.ID) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if !parseForm(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	c, err := p.channels.CreateWithImage(r.Context(), m, name, r.PostForm.Get("image"))
	message, imageMessage := "", ""
	switch {
	case err == nil:
		http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
		return
	case errors.Is(err, conversation.ErrInvalidChannelImage):
		imageMessage = "illustration.error"
	case errors.Is(err, conversation.ErrInvalidChannelName):
		message = "channel.error.name"
	case errors.Is(err, conversation.ErrChannelLimit):
		message = "creation.error.limit"
	case errors.Is(err, conversation.ErrChannelNameTaken):
		message = "channel.error.name_taken"
	default:
		serverError(w, r, "creating channel", err)
		return
	}
	// No channel identity is accepted from the form: failed creation shows the
	// list page with the default channel of the resolved organisation. A phone
	// shows only the list there, so the error stays visible at every width.
	c, err = p.channels.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	p.renderList(w, r, m, c.ID, http.StatusUnprocessableEntity, view.ChannelPage{Name: name, Error: message, Image: r.PostForm.Get("image"), CreateImageError: imageMessage})
}

// list renders the channel list page, nesting the topics of the channel the
// member came from (the query's channel) or else the default channel's.
func (p channelPages) list(w http.ResponseWriter, r *http.Request, m org.Membership) {
	// ParseQuery reports malformed pairs, so a broken link is not the default list.
	query, err := url.ParseQuery(r.URL.RawQuery)
	raw := query["channel"]
	if err != nil || len(raw) > 1 {
		http.NotFound(w, r)
		return
	}
	var id kernel.ID
	if len(raw) == 1 {
		var ok bool
		if id, ok = pathID(raw[0]); !ok {
			http.NotFound(w, r)
			return
		}
	} else {
		c, err := p.channels.Default(r.Context(), m)
		if err != nil {
			serverError(w, r, "finding default channel", err)
			return
		}
		id = c.ID
	}
	p.renderList(w, r, m, id, http.StatusOK, view.ChannelPage{})
}

// renderList keeps the creation form's state; the snapshot checks that the
// channel belongs to the resolved organisation. The list nests the channel's
// topics with none selected and shows no divider, so a sidebar snapshot is the
// whole page, and its stream resumes after the sequence of the snapshot that
// counted, neither skipping nor repeating a change.
func (p channelPages) renderList(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID, status int, page view.ChannelPage) {
	list, err := p.messages.Sidebar(r.Context(), m, id, nil)
	if errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "reading channel list", err)
		return
	}
	p.pages.render(w, r, status, func(url string) templ.Component {
		page.List = true
		page = screenView(r, page, m, list)
		page.EventCursor = &list.SnapshotSeq
		return view.ChannelListScreen(url, page)
	})
}

// screenView fills what every full channel page shows around its content:
// the sidebar, the account menu and the channel image choices.
func screenView(r *http.Request, page view.ChannelPage, m org.Membership, snapshot conversation.SidebarSnapshot) view.ChannelPage {
	account, _ := middleware.Account(r.Context())
	page = sidebarView(page, m, snapshot)
	page.ImageKeys = conversation.ChannelImageKeys()
	page.DisplayName, page.Handle, page.Role = memberDisplayName(account.DisplayName), m.Member.Handle, string(m.Member.Role)
	session, _ := middleware.CurrentSession(r.Context())
	page.Session, page.SessionExpires, page.Rendered = session.ID, session.ExpiresAt, time.Now()
	return page
}

func (p channelPages) render(w http.ResponseWriter, r *http.Request, m org.Membership, id kernel.ID, status int, page view.ChannelPage, firstUnread *int64) {
	var history conversation.ChannelPage
	var err error
	switch {
	case page.Panel == view.PanelOverview:
		// The pins share the page's snapshot, so they agree with its cursor.
		var before *int64
		if page.Before > 0 {
			before = &page.Before
		}
		history, err = p.messages.Overview(r.Context(), m, id, p.topicID, before, page.Around)
	case page.Before > 0:
		history, err = p.messages.Page(r.Context(), m, id, p.topicID, &page.Before)
	case page.Around > 0:
		history, err = p.messages.Around(r.Context(), m, id, p.topicID, page.Around)
	default:
		history, err = p.messages.Page(r.Context(), m, id, p.topicID, nil)
	}
	if errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) || errors.Is(err, conversation.ErrTopicNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "reading channel page", err)
		return
	}
	if page.Panel == view.PanelMembers {
		page.Members, err = p.memberListing(r.Context(), m, id)
		if errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			serverError(w, r, "reading members panel", err)
			return
		}
	}
	page.Pins = viewPins(r.Context(), history.Pins)
	if page.BodyError == "" {
		place := history.Current.DefaultTopicID
		if history.Topic != nil {
			place = history.Topic.ID
		}
		page.Composer, page.IdempotencyKey = p.issueComposer(m, history.Current.ID, place), freshKey()
	}
	var snapshotSeq int64
	if history.EventCursor != nil {
		snapshotSeq = *history.EventCursor
	}
	page.Messages = make([]view.Message, len(history.Entries))
	for i, entry := range history.Entries {
		page.Messages[i] = viewMessage(m.Organization.Slug, entry)
	}
	p.pages.render(w, r, status, func(url string) templ.Component {
		page = screenView(r, page, m, conversation.SidebarSnapshot{
			Current: history.Current, Channels: history.Channels, Topic: history.Topic, Topics: history.Topics, SnapshotSeq: snapshotSeq,
			ChannelCounts: history.ChannelCounts, TopicCounts: history.TopicCounts,
		})
		page.Older = history.Older
		page.FirstUnread = history.FeedFirstUnread
		if history.Topic != nil {
			page.FirstUnread = history.TopicFirstUnread
		}
		if firstUnread != nil {
			page.FirstUnread = *firstUnread
		}
		// Links carry the divider this page shows, as the older link does.
		page.Query = conversationState(position{before: page.Before, around: page.Around, firstUnread: &page.FirstUnread})
		page.Reading = p.reading != nil
		page.EventCursor = history.EventCursor
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
	var cursor *int64
	if raw, present := r.PostForm["cursor"]; present {
		value, err := strconv.ParseInt(r.PostForm.Get("cursor"), 10, 64)
		if err != nil || len(raw) != 1 {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		cursor = &value
	}
	// A key deduplicates repeated sends (decision 37 (b)); pages rendered
	// before keys existed send none and post without deduplication.
	var key *conversation.IdempotencyKey
	submittedKey, keyed := r.PostForm["idempotency_key"]
	if keyed {
		parsed, ok := pathID(submittedKey[0])
		if !ok || len(submittedKey) != 1 {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		key = new(conversation.IdempotencyKey(parsed))
	}
	body := r.PostForm.Get("body")
	var composer *string
	if _, present := r.PostForm["composer"]; present {
		composer = new(r.PostForm.Get("composer"))
	}
	sent, err := p.posting.Send(r.Context(), m, c.ID, conversation.Send{Topic: p.topicID, Body: body, Cursor: cursor, Key: key})
	posted := sent.Message
	// A repeat closes the generation in the requested place it was issued
	// for, not in the topic branching may have moved the message into.
	if err == nil && p.typing != nil {
		p.typing.Posted(posted.OrganizationID, typing.Place{Channel: posted.ChannelID, Topic: sent.Topic}, posted.MemberID, r.PostForm.Get("composer"))
	}
	enhanced := r.Header.Get("HX-Request") == "true"
	topic := p.topicID
	if errors.Is(err, conversation.ErrInvalidBody) && enhanced && topic != nil {
		// Validation fails before posting resolves the topic, and the
		// fragment reads no history that would: resolve it here, so an
		// unknown or other channel's topic stays a 404 without a fragment.
		if resolved, lookupErr := p.topics.Get(r.Context(), m, c.ID, *topic); lookupErr != nil {
			err = lookupErr
		} else {
			topic = &resolved.ID
		}
	} else if topic != nil {
		// The posting transaction resolved the requested topic; reusing it
		// keeps issuance free of another lookup.
		topic = &sent.Topic
	}
	switch {
	case errors.Is(err, conversation.ErrInvalidPostCursor):
		http.Error(w, "Bad Request", http.StatusBadRequest)
	case errors.Is(err, conversation.ErrInvalidBody):
		var kept *string
		if keyed {
			kept = &submittedKey[0]
		}
		// One message for a missing and another organisation's member, so
		// the answer tells nothing about other organisations.
		bodyError := "message.error.body"
		if errors.Is(err, conversation.ErrUnknownReference) {
			bodyError = "message.error.reference"
		}
		p.renderComposer(w, r, m, c, topic, http.StatusUnprocessableEntity, view.ChannelPage{Composer: composer, EventCursor: cursor, Body: body, BodyError: bodyError, IdempotencyKey: kept})
	case errors.Is(err, conversation.ErrPostKeyReused):
		// Not a retry: keep the text, name the stored message and issue a
		// fresh key, so sending again posts the new text.
		p.renderComposer(w, r, m, c, topic, http.StatusConflict, view.ChannelPage{Composer: composer, EventCursor: cursor, Body: body, BodyError: "message.error.repeat", PostedMessageID: &posted.ID, IdempotencyKey: freshKey()})
	case errors.Is(err, conversation.ErrChannelNotFound), errors.Is(err, org.ErrNotFound), errors.Is(err, conversation.ErrTopicNotFound):
		// The channel, membership or organisation went away after this
		// request resolved them; answer as for a non-member.
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "posting message", err)
	case enhanced:
		p.renderComposer(w, r, m, c, topic, http.StatusOK, view.ChannelPage{EventCursor: cursor, PostedMessageID: &posted.ID})
	default:
		http.Redirect(w, r, view.ConversationURL(m.Organization.Slug, c.ID, p.topicID), http.StatusSeeOther)
	}
}

// Enhanced posts never replace history or the connection's snapshot cursor.
// topic is the resolved posting topic of a topic view, nil on the feed; the
// fragment's typing form signals there and its generation is scoped to it,
// while the page's message form keeps its own action.
func (p channelPages) renderComposer(w http.ResponseWriter, r *http.Request, m org.Membership, c conversation.Channel, topic *kernel.ID, status int, page view.ChannelPage) {
	if r.Header.Get("HX-Request") == "true" {
		place := c.DefaultTopicID
		if topic != nil {
			place, page.Topic = *topic, &view.Topic{ID: *topic}
		}
		if page.BodyError == "" {
			page.Composer, page.IdempotencyKey = p.issueComposer(m, c.ID, place), freshKey()
		}
		page.Organization, page.Current = view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name}, viewChannel(c)
		templ.Handler(view.MessageComposer(page), templ.WithStatus(status)).ServeHTTP(w, r)
		return
	}
	p.render(w, r, m, c.ID, status, page, nil)
}

// Reuse resolved scope: issuance needs no channel or topic lookup of its own.
func (p channelPages) issueComposer(m org.Membership, channel, topic kernel.ID) *string {
	if p.typing == nil {
		return nil
	}
	return new(p.typing.IssueComposer(m.Organization.ID, typing.Place{Channel: channel, Topic: topic}, m.Member.ID))
}

// freshKey renders a new composer key: on every render but a 422, which
// keeps the submitted one so that a corrected send is still deduplicated.
func freshKey() *string {
	return new(conversation.NewIdempotencyKey().String())
}

// channelID parses the {channelID} path segment: the UUID in its canonical
// lower-case form, the only one ChannelURL produces; any other spelling is
// malformed.
func channelID(r *http.Request) (kernel.ID, bool) {
	return pathID(r.PathValue("channelID"))
}

// pathID parses an identifier from a path segment, query or form value.
// It accepts only the canonical lower-case form the views render, so one
// resource has one URL (decision 36): hex.DecodeString would also take upper
// case, which is refused here like any malformed identifier rather than
// redirected, since nothing in ribbitto produces it (#1223).
func pathID(raw string) (kernel.ID, bool) {
	var id kernel.ID
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return id, false
	}
	if strings.ToLower(raw) != raw {
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
	return view.Channel{ID: c.ID, DefaultTopicID: c.DefaultTopicID, Name: c.Name, ImageURL: channelImageURL(c.ID, c.Image), Image: c.Image, Created: c.CreatedAt}
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
		ID: entry.ID, MemberID: entry.MemberID, DisplayName: memberDisplayName(entry.DisplayName), Handle: entry.Handle,
		CreatedAt: entry.CreatedAt, Body: entry.Body, EventSeq: entry.EventSeq,
		TopicID: entry.TopicID, TopicName: entry.TopicName, DefaultTopic: entry.DefaultTopic,
		TopicURL: view.ConversationURL(slug, entry.ChannelID, &entry.TopicID), URL: view.MessageURL(slug, entry.ID),
		PinURL: view.PinURL(slug, entry.ChannelID, entry.ID), Pinned: entry.Pinned, Blocks: viewBody(entry),
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
