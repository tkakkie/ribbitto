package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type fakeMessages struct {
	channels *fakeChannels
	entries  []conversation.Entry
	older    bool
	err      error
	// firstUnread is the snapshot's feed divider.
	firstUnread int64
	// before, when set, records the bound each read received.
	before *[]*int64
	// around, when set, records each around read's position.
	around *[]int64
	// located answers Locate; nil is ErrMessageNotFound.
	located *conversation.Location
	// pins are the overview's pins.
	pins []conversation.Pin
}

func (f fakeMessages) Around(ctx context.Context, m org.Membership, id kernel.ID, topicID *kernel.ID, around int64) (conversation.ChannelPage, error) {
	if f.around != nil {
		*f.around = append(*f.around, around)
	}
	f.before = nil
	return f.Page(ctx, m, id, topicID, nil)
}

func (f fakeMessages) Locate(context.Context, org.Membership, kernel.ID) (conversation.Location, error) {
	if f.located == nil {
		return conversation.Location{}, conversation.ErrMessageNotFound
	}
	return *f.located, f.err
}

// One is not used by handler tests; the stream is tested end to end.
func (fakeMessages) Many(context.Context, org.Membership, kernel.ID, []kernel.ID) ([]conversation.Entry, error) {
	return nil, conversation.ErrMessageNotFound
}

func (fakeMessages) One(context.Context, org.Membership, kernel.ID, int64) (conversation.Entry, error) {
	return conversation.Entry{}, conversation.ErrMessageNotFound
}

func (f fakeMessages) Page(ctx context.Context, m org.Membership, id kernel.ID, topicID *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	if f.before != nil {
		*f.before = append(*f.before, before)
	}
	channels := f.channels
	if channels == nil {
		channels = &fakeChannels{}
	}
	current, err := channels.Get(ctx, m, id)
	if err != nil {
		return conversation.ChannelPage{}, err
	}
	list, err := channels.list(ctx, m)
	if err != nil {
		return conversation.ChannelPage{}, err
	}
	page := conversation.ChannelPage{Page: conversation.Page{Entries: f.entries, Older: f.older}, Current: current, Channels: list}
	page.FeedFirstUnread = f.firstUnread
	if topicID != nil {
		page.Topic = &conversation.Topic{ID: *topicID, Name: "Planning"}
	}
	cursor := int64(42)
	page.EventCursor = &cursor
	return page, f.err
}

func populatedMessages() fakeMessages {
	return fakeMessages{entries: []conversation.Entry{
		{Message: conversation.Message{ID: kernel.ID{8}, ChannelID: kernel.ID{1}, TopicID: kernel.ID{0x31}, EventSeq: 7, Body: "<script>bad()</script>\nمرحبا\u2069", CreatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}, DisplayName: "مريم", Handle: "author", TopicName: "<design>مرحبا"},
		{Message: conversation.Message{ID: kernel.ID{9}, ChannelID: kernel.ID{1}, TopicID: kernel.ID{0x32}, EventSeq: 8, Body: "second", CreatedAt: time.Now()}, DisplayName: "\u3164", Handle: "legacy", DefaultTopic: true},
	}}
}

func olderMessages() fakeMessages {
	f := populatedMessages()
	f.older = true
	return f
}

func TestMessageListHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		reader fakeMessages
		status int
	}{
		{"populated", populatedMessages(), 200}, {"empty", fakeMessages{}, 200}, {"read failure", fakeMessages{err: errors.New("offline")}, 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages = tt.reader
				s.Channels = &fakeChannels{getErr: errors.New("unexpected separate channel read")}
			}))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", view.ChannelURL("acme", kernel.ID{1}), nil)
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if tt.status != 200 {
				return
			}
			body := w.Body.String()
			if strings.Contains(body, "data-announcement") {
				t.Fatal("history and Load older responses must not carry announcement text")
			}
			assertFullConversationPage(t, body)
			doc, err := html.Parse(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if outer := find(doc, atom.Div); attr(outer, "data-event-cursor") != "42" || attr(outer, "sse-connect") != "/organizations/acme/events?after=42&want=sidebar,messages,typing&channel=01000000-0000-0000-0000-000000000000" || attr(outer, "hx-ext") != "sse" || attr(outer, "id") != "organization-stream" || outer.Parent.DataAtom != atom.Body {
				t.Fatal("cursor must be on the outer layout, outside every swap target")
			}
			if items := find(doc, atom.Ol); attr(items, "sse-swap") != "message,messages-moved" {
				t.Fatal("latest message list must receive message events")
			}
			// An empty channel still loads it: messages swapped in later need it.
			if script := `src="/static/message-time-v2.js" nonce="` + responseNonce(t, w) + `"`; !strings.Contains(body, script) {
				t.Errorf("missing %q", script)
			}
			if len(tt.reader.entries) == 0 {
				return
			}
			for _, want := range []string{fmt.Sprintf(`id="message-%x"`, kernel.ID{8}), `dir="auto"`, `whitespace-pre-wrap`, "&lt;script&gt;bad()&lt;/script&gt;\nمرحبا\u2069", `datetime="2026-09-29T12:00:00Z"`, "2026-09-29 12:00:00 UTC", `>مريم</bdi>`, "@author", "@legacy"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Contains(body, "\u3164") || strings.Contains(body, "<script>bad()") {
				t.Fatal("unsafe body or blank-looking name")
			}
		})
	}
}

func TestMessageTimestampView(t *testing.T) {
	for _, tt := range []struct {
		name     string
		created  time.Time
		datetime string
	}{
		{"whole second", time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), "2026-09-29T12:00:00Z"},
		{"microseconds", time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC), "2026-09-29T12:00:00.123Z"},
		{"no rounding", time.Date(2026, 9, 29, 12, 0, 0, 999999000, time.UTC), "2026-09-29T12:00:00.999Z"},
		{"offset to UTC", time.Date(2026, 9, 29, 21, 0, 0, 123456000, time.FixedZone("JST", 9*60*60)), "2026-09-29T12:00:00.123Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page := view.ChannelPage{
				Organization: view.Organization{Name: "Acme", Slug: "acme"},
				Messages:     []view.Message{{CreatedAt: tt.created}},
			}
			var b strings.Builder
			if err := view.ChannelScreen("", page).Render(context.Background(), &b); err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(strings.NewReader(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			element := find(doc, atom.Time)
			if element == nil {
				t.Fatal("missing timestamp")
			}
			if got := attr(element, "datetime"); got != tt.datetime {
				t.Errorf("datetime = %q, want %q", got, tt.datetime)
			}
			if got := text(element); got != "2026-09-29 12:00:00 UTC" {
				t.Errorf("UTC fallback = %q", got)
			}
			if page.Messages[0].CreatedAt != tt.created {
				t.Error("rendering changed the message timestamp")
			}
		})
	}
}

type fakeTxRunner struct{}

func (fakeTxRunner) InTx(_ context.Context, fn func(platform.Tx) error) error {
	return fn(platform.Tx{})
}

type fakePostingWriter struct {
	returned             *conversation.Message
	id                   kernel.ID
	err                  error
	body                 string
	org, channel, member kernel.ID
	// keyed holds the author's messages by key, as the unique index does;
	// fakeTxRunner has no rollback, so a taken key writes no field above.
	keyed   map[[3][16]byte]conversation.Message
	inserts int
}

func (*fakePostingWriter) GetDefaultTopic(_ context.Context, org, channel kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: kernel.ID{2}, OrganizationID: org, ChannelID: channel, IsDefault: true}, nil
}

func (*fakePostingWriter) GetTopic(_ context.Context, org, channel, id kernel.ID) (conversation.Topic, error) {
	return conversation.Topic{ID: id, OrganizationID: org, ChannelID: channel}, nil
}

func (w *fakePostingWriter) InsertMessage(_ context.Context, org, channel, topic, member kernel.ID, body string, seq int64, key *conversation.IdempotencyKey, proof conversation.TypedBodyProof, _ *conversation.Quote) (conversation.Message, error) {
	if _, taken := w.keyed[[3][16]byte{org, member, keyBytes(key)}]; key != nil && taken {
		return conversation.Message{}, conversation.ErrPostKeyTaken
	}
	w.org, w.channel, w.member, w.body = org, channel, member, body
	if w.err != nil {
		return conversation.Message{}, w.err
	}
	w.inserts++
	posted := conversation.Message{ID: w.id, OrganizationID: org, ChannelID: channel, TopicID: topic, MemberID: member, Body: body, TypedBodyProof: proof, EventSeq: seq}
	if w.returned != nil {
		posted = *w.returned
	}
	if key != nil {
		if w.keyed == nil {
			w.keyed = map[[3][16]byte]conversation.Message{}
		}
		w.keyed[[3][16]byte{org, member, *key}] = posted
	}
	return posted, nil
}

func (w *fakePostingWriter) KeyedMessage(_ context.Context, org, member kernel.ID, key conversation.IdempotencyKey) (conversation.Message, error) {
	if m, ok := w.keyed[[3][16]byte{org, member, key}]; ok {
		return m, nil
	}
	return conversation.Message{}, conversation.ErrMessageNotFound
}

func keyBytes(key *conversation.IdempotencyKey) [16]byte {
	if key == nil {
		return [16]byte{}
	}
	return *key
}

// Branching's methods must fail if a posting test calls them.
func (*fakePostingWriter) CreateTopic(context.Context, kernel.ID, kernel.ID, string) (conversation.Topic, error) {
	panic("posting fake: unexpected CreateTopic")
}

func (*fakePostingWriter) QuotedMessage(context.Context, kernel.ID, kernel.ID, kernel.ID) (conversation.Message, error) {
	panic("posting fake: unexpected QuotedMessage")
}

func (*fakePostingWriter) InsertNotice(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, string, int64) (conversation.Message, error) {
	panic("posting fake: unexpected InsertNotice")
}

func (*fakePostingWriter) MoveMessages(context.Context, kernel.ID, kernel.ID, kernel.ID, kernel.ID, []kernel.ID, int64) (int64, error) {
	panic("posting fake: unexpected MoveMessages")
}

type fakeEventSequence struct{}

func (fakeEventSequence) NextEventSeq(context.Context, kernel.ID) (int64, error) {
	return 43, nil
}

type fakeEventAppender struct{}

func (fakeEventAppender) Append(context.Context, kernel.ID, int64, realtime.EventKind, *kernel.ID, []byte) error {
	return nil
}

var (
	_ conversation.TxRunner      = fakeTxRunner{}
	_ conversation.Writer        = (*fakePostingWriter)(nil)
	_ conversation.EventSequence = fakeEventSequence{}
	_ conversation.EventAppender = fakeEventAppender{}
)

func testPosting(writer conversation.Writer) *conversation.Posting {
	return conversation.NewPagePosting(fakeTxRunner{},
		func(platform.Tx) conversation.Writer { return writer },
		func(platform.Tx) conversation.EventSequence { return fakeEventSequence{} },
		func(platform.Tx) conversation.EventAppender { return fakeEventAppender{} }, func(platform.Tx) conversation.PostReadWriter { return fakePostReads{} }, nil, nil, nil, nil)
}

func testPoster() *conversation.Posting { return testPosting(&fakePostingWriter{}) }

func TestMessagePostHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, body string
		storeErr   error
		status     int
	}{
		{"success", "  hello\r\nworld  ", nil, 303},
		{"empty", " \n ", nil, 422},
		{"leading newline", "\n\n", nil, 422},
		{"long", strings.Repeat("界", 4001), nil, 422},
		{"forbidden", "<script>\u202ebad</script>", nil, 422},
		{"missing", "hello", conversation.ErrChannelNotFound, 404},
		{"membership gone", "hello", org.ErrNotFound, 404},
		{"failure", "hello", errors.New("offline"), 500},
	} {
		for _, mode := range []struct{ hx, topic bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
			hx := mode.hx
			t.Run(fmt.Sprintf("%s/htmx=%t/topic=%t", tt.name, hx, mode.topic), func(t *testing.T) {
				writer := &fakePostingWriter{id: kernel.ID{37}, err: tt.storeErr}
				state := typing.New(realtime.NewHub())
				actual := conversation.Message{ID: writer.id, OrganizationID: kernel.ID{7}, ChannelID: kernel.ID{8}, TopicID: kernel.ID{6}, MemberID: kernel.ID{5}, EventSeq: 71}
				writer.returned = &actual
				// Posting closes the generation in the requested place: the
				// fake's default topic and the topic URL's are both {2}, and
				// typing never uses the returned message's topic {6} (#1155).
				place := typing.Place{Channel: actual.ChannelID, Topic: kernel.ID{2}}
				other := typing.Place{Channel: place.Channel, Topic: kernel.ID{3}}
				defer state.Open(actual.OrganizationID, actual.MemberID, place.Channel)()
				token := state.IssueComposer(actual.OrganizationID, place, actual.MemberID)
				state.Signal(actual.OrganizationID, place, typing.Typist{Member: actual.MemberID}, token, time.Now())
				state.Start(actual.OrganizationID, other, typing.Typist{Member: actual.MemberID})
				before := state.Read(actual.OrganizationID, place, kernel.ID{})
				var reads []*int64
				h, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
					reader := populatedMessages()
					reader.before = &reads
					s.Messages, s.Posting = reader, testPosting(writer)
					s.Typing, s.SignalLimits = state, middleware.NewMemberRateLimiter(time.Now)
				}))
				if err != nil {
					t.Fatal(err)
				}
				path := view.ChannelURL("acme", kernel.ID{1})
				if mode.topic {
					path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2})
				}
				r := httptest.NewRequest("POST", path+"?body=wrong", strings.NewReader(url.Values{"composer": {token}, "cursor": {"0"}, "body": {tt.body}, "organization_id": {"other"}, "member_id": {"other"}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if hx {
					r.Header.Set("HX-Request", "true")
				}
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := tt.status
				if hx && want == 303 {
					want = 200
				}
				if w.Code != want {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				if tt.status != 303 && !reflect.DeepEqual(before, state.Read(actual.OrganizationID, place, kernel.ID{})) {
					t.Fatal("failed post changed activity before another signal")
				}
				state.Signal(actual.OrganizationID, place, typing.Typist{Member: actual.MemberID}, token, time.Now())
				if got := state.Read(actual.OrganizationID, place, kernel.ID{9}); (len(got.Typists) == 0) != (tt.status == 303) {
					t.Fatal("post-success stop or stale-signal ordering failed")
				}
				if len(state.Read(actual.OrganizationID, typing.Place{Channel: place.Channel}, kernel.ID{9}).Typists) != 1 {
					t.Fatal("post cleared another active topic")
				}
				state.Signal(actual.OrganizationID, place, typing.Typist{Member: actual.MemberID}, state.IssueComposer(actual.OrganizationID, place, actual.MemberID), time.Now())
				if len(state.Read(actual.OrganizationID, place, kernel.ID{9}).Typists) != 1 {
					t.Fatal("new composer cannot start typing")
				}
				if tt.status == 303 {
					if writer.body != "hello\nworld" || writer.org != (kernel.ID{}) || writer.member != (kernel.ID{}) || writer.channel != (kernel.ID{1}) {
						t.Fatalf("posting scope/body: %+v", writer)
					}
					if !hx && w.Header().Get("Location") != path {
						t.Fatal("wrong redirect")
					}
				}
				marker := `data-posted-message="` + view.MessageDOMID(writer.id) + `"`
				if hx && (want == 200 || want == 422) && !strings.Contains(w.Body.String(), `name="cursor" value="0"`) {
					t.Fatal("composer must echo submitted cursor, not post sequence")
				}
				if hx && want == 200 {
					if !strings.Contains(w.Body.String(), `data-posted-topic="06000000-0000-0000-0000-000000000000"`) || !strings.Contains(w.Body.String(), `data-posted-seq="71"`) {
						t.Fatal("success must carry the returned message's topic and posting sequence")
					}
					if !strings.Contains(w.Body.String(), marker) {
						t.Fatal("success must identify the posted message for stream correlation")
					}
				} else if strings.Contains(w.Body.String(), "data-posted-message") {
					t.Fatal("only enhanced success may carry a posted message ID")
				}
				if want != 422 && want != 200 {
					return
				}
				if hx {
					if len(reads) != 0 || strings.Contains(w.Body.String(), `id="conversation"`) || !strings.HasPrefix(w.Body.String(), composerFragment) {
						t.Fatal("enhanced post must render only the composer state without reading history")
					}
					// A topic fragment signals the requested topic, never the
					// returned message's (the fake returns another topic).
					typingTarget := view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{}) + "/typing" // The fake channel's default topic.
					if mode.topic {
						typingTarget = path + "/typing"
					}
					state, signal := enhancedForms(t, w.Body.String())
					if attr(signal, "hx-post") != typingTarget {
						t.Fatalf("fragment signals %q, want %q", attr(signal, "hx-post"), typingTarget)
					}
					// The script copies this flag to the page's textarea, which
					// the answer never replaces (#1173).
					if _, invalid := attrOK(state, "data-invalid"); invalid != (want == 422) || find(state, atom.Textarea) != nil {
						t.Fatal("enhanced answer flags the field wrongly or replaces the textarea")
					}
					if want == 422 && (attr(find(state, atom.P), "role") != "alert" || writer.body != "") {
						t.Fatal("enhanced 422 lacks its alert or posted")
					}
					return
				} else {
					assertFullConversationPage(t, w.Body.String())
				}
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				field := find(doc, atom.Textarea)
				if want == 422 {
					if text(field) != tt.body || attr(field, "aria-invalid") != "true" || writer.body != "" {
						t.Fatal("invalid input lost or posted")
					}
					checkFieldError(t, doc, field)
				} else if text(field) != "" {
					t.Fatal("composer not cleared")
				}
			})
		}
	}
}

// enhancedForms returns a composer fragment's state and its out-of-band
// signal form.
func enhancedForms(t *testing.T, body string) (state, signal *html.Node) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		switch attr(n, "id") {
		case "message-composer-state":
			state = n
		case "typing-form":
			signal = n
		}
	}
	if state == nil || signal == nil {
		t.Fatal("fragment lacks the composer state or signal form")
	}
	return state, signal
}

func TestMessagePagingHandler(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	channelURL := view.ChannelURL("acme", kernel.ID{1})
	topicID := kernel.ID{5}
	topicURL := view.ConversationURL("acme", kernel.ID{1}, &topicID)
	for _, tt := range []struct {
		name, query string
		older       bool
		status      int
		before      int64 // 0: the latest page; the around position on an around page
		want, avoid []string
		htmx        bool
		path        string
		wantTopic   string
		wantPost    string
	}{
		{"latest with older", "", true, 200, 0, []string{`href="` + channelURL + `?before=7&amp;unread=0"`, `hx-get="` + channelURL + `?before=7&amp;unread=0"`, `hx-select-oob="#load-older"`, `id="load-older"`}, []string{"Jump to the newest"}, false, channelURL, "", channelURL},
		{"latest without older", "", false, 200, 0, []string{`<div id="load-older" data-oldest-seq="0"></div>`}, []string{"?before=", "Jump to the newest"}, false, channelURL, "", channelURL},
		{"older page", "?before=40", true, 200, 40, []string{`?before=7&amp;unread=0"`, `Jump to the newest ribbits</a>`}, nil, false, channelURL, "", ""},
		{"older page with HX", "?before=40", true, 200, 40, []string{`?before=7&amp;unread=0"`, `Jump to the newest ribbits</a>`}, nil, true, channelURL, "", ""},
		{"oldest page", "?before=8", false, 200, 8, []string{`Jump to the newest ribbits</a>`, `<div id="load-older" data-oldest-seq="0"></div>`}, []string{"Load older ribbits", "hx-get="}, false, channelURL, "", ""},
		{"oldest page with HX", "?before=8", false, 200, 8, []string{`<div id="load-older" data-oldest-seq="0"></div>`}, []string{"Load older ribbits", "hx-get="}, true, channelURL, "", ""},
		{"zero", "?before=0", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"negative", "?before=-3", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"not a number", "?before=abc", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"empty", "?before=", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"repeated", "?before=5&before=6", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"overflow", "?before=9223372036854775808", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"malformed escape", "?before=%ZZ", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"repeated with a malformed escape", "?before=5&before=%ZZ", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"latest topic", "", true, 200, 0, []string{`href="` + topicURL + `?before=7&amp;unread=0"`, `hx-get="` + topicURL + `?before=7&amp;unread=0"`}, []string{"Jump to the newest"}, false, topicURL, "05000000-0000-0000-0000-000000000000", topicURL},
		// An around page (decision 36) is an older page in every check below.
		{"around page", "?around=40", true, 200, 40, []string{`?before=7&amp;unread=0"`, `Jump to the newest ribbits</a>`}, nil, false, channelURL, "", ""},
		{"around topic with divider", "?around=40&unread=3", true, 200, 40, []string{`href="` + topicURL + `?before=7&amp;unread=3"`, `Jump to the newest ribbits</a>`}, nil, false, topicURL, "05000000-0000-0000-0000-000000000000", ""},
		{"around zero", "?around=0", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"around not a number", "?around=abc", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"around repeated", "?around=5&around=6", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"around with before", "?around=5&before=6", false, 400, -1, nil, nil, false, channelURL, "", ""},
		{"older topic", "?before=40", true, 200, 40, []string{`href="` + topicURL + `?before=7&amp;unread=0"`, `hx-get="` + topicURL + `?before=7&amp;unread=0"`, `Jump to the newest ribbits</a>`}, nil, false, topicURL, "05000000-0000-0000-0000-000000000000", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var seen []*int64
			var arounds []int64
			reader := fakeMessages{entries: []conversation.Entry{{Message: conversation.Message{ID: kernel.ID{8}, TopicID: kernel.ID{5}, EventSeq: 7, Body: "oldest shown"}, DisplayName: "A", Handle: "a"}}, older: tt.older, before: &seen, around: &arounds}
			handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) {
				s.Messages, s.Reading = reader, readCall(func(context.Context) error { return nil })
			}))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", tt.path+tt.query, nil)
			if tt.htmx {
				req.Header.Set("HX-Request", "true")
			}
			req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if tt.status != 200 {
				if len(seen) != 0 || len(arounds) != 0 {
					t.Fatal("a malformed bound reached the reader")
				}
				return
			}
			if strings.HasPrefix(tt.query, "?around=") {
				if len(seen) != 0 || len(arounds) != 1 || arounds[0] != tt.before {
					t.Fatalf("reader bounds %v, around %v", seen, arounds)
				}
			} else if len(seen) != 1 || (tt.before == 0) != (seen[0] == nil) || (seen[0] != nil && *seen[0] != tt.before) || len(arounds) != 0 {
				t.Fatalf("reader bounds %v, around %v", seen, arounds)
			}
			// An older page neither reads nor shows typing.
			if body := w.Body.String(); strings.Contains(body, `id="read-form"`) == (tt.before > 0) || strings.Contains(body, `id="typing-status"`) == (tt.before > 0) {
				t.Fatalf("read form or typing status on an older page, or missing on the latest, before = %d", tt.before)
			}
			body := w.Body.String()
			if strings.Contains(body, "data-announcement") {
				t.Fatal("history and Load older responses must not carry announcement text")
			}
			assertFullConversationPage(t, body)
			// The data attributes of the DOM contract (#351), on full pages
			// and on Load older responses (the HX cases).
			page, err := html.Parse(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			oldest := "0"
			if tt.older {
				oldest = "7"
			}
			if got, ok := idAttribute(t, page, "load-older", "data-oldest-seq"); !ok || got != oldest {
				t.Errorf("data-oldest-seq = %q, %t; want %q", got, ok, oldest)
			}
			item := fmt.Sprintf("message-%x", kernel.ID{8})
			if got, ok := idAttribute(t, page, item, "data-event-seq"); !ok || got != "7" {
				t.Errorf("data-event-seq = %q, %t; want 7", got, ok)
			}
			if got, ok := idAttribute(t, page, "select-"+item, "data-source"); !ok || got != "05000000-0000-0000-0000-000000000000" {
				t.Errorf("data-source = %q, %t; want the message's topic", got, ok)
			}
			if got, ok := idAttribute(t, page, "message-items", "data-topic"); got != tt.wantTopic || ok != (tt.wantTopic != "") {
				t.Errorf("data-topic = %q, %t; want %q", got, ok, tt.wantTopic)
			}
			if got, ok := idAttribute(t, page, "message-composer", "hx-post"); got != tt.wantPost || ok != (tt.wantPost != "") {
				t.Errorf("composer hx-post = %q, %t; want %q", got, ok, tt.wantPost)
			}
			var hasCursor bool
			for form := range page.Descendants() {
				if attr(form, "id") == "message-composer" {
					for n := range form.Descendants() {
						if n.DataAtom == atom.Input && attr(n, "name") == "cursor" {
							hasCursor = true
						}
					}
				}
			}
			if hasCursor != (tt.before == 0) {
				t.Fatalf("composer cursor present = %t, before = %d", hasCursor, tt.before)
			}
			if got := strings.Contains(body, `data-event-cursor="42"`); !got {
				t.Fatalf("page cursor present = %t, before = %d", got, tt.before)
			}
			want := "sidebar"
			if tt.before == 0 {
				want += ",messages,typing"
			}
			endpoint := "/organizations/acme/events?after=42&want=" + want + "&channel=01000000-0000-0000-0000-000000000000"
			if tt.wantTopic != "" {
				endpoint += "&topic=" + tt.wantTopic
			}
			if got, ok := idAttribute(t, page, "organization-stream", "sse-connect"); !ok || got != endpoint {
				t.Errorf("stream URL = %q, %t; want %q", got, ok, endpoint)
			}
			if got, ok := idAttribute(t, page, "message-items", "sse-swap"); ok != (tt.before == 0) || (ok && got != "message,messages-moved") {
				t.Errorf("message sse-swap = %q, %t, before = %d", got, ok, tt.before)
			}
			if tt.before > 0 {
				doc, err := html.Parse(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				for n := range doc.Descendants() {
					if n.DataAtom == atom.A && strings.TrimSpace(text(n)) == "Jump to the newest ribbits" && attr(n, "href") != tt.path {
						t.Errorf("newest-page link = %q; want %q", attr(n, "href"), tt.path)
					}
					if attr(n, "id") != "message-composer" {
						continue
					}
					posted := serveForm(handler, "POST", attr(n, "action"), "live", url.Values{"body": {"from older page"}})
					if posted.Code != 303 || posted.Header().Get("Location") != tt.path {
						t.Fatal("older-page form must redirect to the latest page")
					}
					invalid := serveForm(handler, "POST", attr(n, "action"), "live", url.Values{"body": {"\n\n"}})
					if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), "\n\n\n</textarea>") {
						t.Fatal("older-page form must retain an invalid draft")
					}
					assertFullConversationPage(t, invalid.Body.String())
				}
			}
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, avoid := range tt.avoid {
				if strings.Contains(body, avoid) {
					t.Errorf("unexpected %q", avoid)
				}
			}
		})
	}
}

func TestFeedTopicLabels(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = populatedMessages() }))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		for _, suffix := range []string{"", "?before=9"} {
			t.Run(lang+suffix, func(t *testing.T) {
				req := httptest.NewRequest("GET", view.ChannelURL("acme", kernel.ID{1})+suffix, nil)
				req.Header.Set("Accept-Language", lang)
				req.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				body := w.Body.String()
				if w.Code != 200 {
					t.Fatalf("feed labels: %d, %s", w.Code, body)
				}
				// Each label links to its topic view (#303).
				for _, entry := range populatedMessages().entries {
					assertMessageTopicLink(t, body, entry)
				}
				catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					for _, entry := range populatedMessages().entries {
						var live bytes.Buffer
						if err := view.LiveMessageItem(viewMessage("acme", entry)).Render(r.Context(), &live); err != nil {
							t.Fatal(err)
						}
						assertMessageTopicLink(t, live.String(), entry)
					}
				})).ServeHTTP(httptest.NewRecorder(), req)
			})
		}
	}
}

// Labels must point at their message's topic even when an icon precedes them.
func assertMessageTopicLink(t *testing.T, markup string, entry conversation.Entry) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	label := "<design>مرحبا"
	if entry.DefaultTopic {
		label = "chorus"
	}
	url := view.ConversationURL("acme", entry.ChannelID, &entry.TopicID)
	for n := range doc.Descendants() {
		if n.DataAtom == atom.A && attr(n, "href") == url {
			if name := find(n, atom.Bdi); name != nil && text(name) == label {
				return
			}
		}
	}
	t.Fatalf("topic label %q must link to %s", label, url)
}

func (f fakeMessages) Overview(ctx context.Context, m org.Membership, id kernel.ID, topicID *kernel.ID, before *int64, around int64) (conversation.ChannelPage, error) {
	// Like Reader.Overview, a positive around reads Around's page, so the
	// positions a test records are the ones the page asked for.
	read := func() (conversation.ChannelPage, error) { return f.Page(ctx, m, id, topicID, before) }
	if around > 0 {
		read = func() (conversation.ChannelPage, error) { return f.Around(ctx, m, id, topicID, around) }
	}
	page, err := read()
	page.Pins = f.pins
	return page, err
}

func (f fakeMessages) Members(ctx context.Context, m org.Membership, id kernel.ID, _ *kernel.ID) (conversation.MembersPage, error) {
	// Like Reader.Members, a channel the page cannot find is an error.
	_, err := f.Page(ctx, m, id, nil, nil)
	return conversation.MembersPage{Members: []conversation.ChannelMember{{ID: kernel.ID{1}, DisplayName: "مريم", Handle: "alice"}, {ID: kernel.ID{2}, DisplayName: "\u3164", Handle: "legacy"}}, Next: &kernel.ID{7}}, err
}

func (*fakePostingWriter) LastMessageBefore(context.Context, kernel.ID, kernel.ID, int64) (int64, error) {
	return 0, nil
}

func (w *fakePostingWriter) TopicChangedBetween(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64) (bool, error) {
	return false, nil
}

type fakePostReads struct{}

func (fakePostReads) Read(context.Context, org.Membership, kernel.ID, *kernel.ID, *int64, int64, int64, int64) error {
	return nil
}

func (f fakeMessages) Sidebar(ctx context.Context, m org.Membership, id kernel.ID, topic *kernel.ID) (conversation.SidebarSnapshot, error) {
	page, err := f.Page(ctx, m, id, topic, nil)
	return conversation.SidebarSnapshot{Current: page.Current, Channels: page.Channels, Topics: page.Topics, SnapshotSeq: 42}, err
}

func (f fakeMessages) ReadPins(context.Context, org.Membership, kernel.ID) (conversation.PinsSnapshot, error) {
	return conversation.PinsSnapshot{Pins: f.pins, SnapshotSeq: 42}, nil
}
