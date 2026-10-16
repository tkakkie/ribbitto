package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

var canonicalKey = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// keyedComposer serves the composer handler over a posting fake with the
// key index, on the feed or a topic.
func keyedComposer(t *testing.T, topic bool) (http.Handler, *fakePostingWriter, *typing.State, org.Membership, typing.Place, string) {
	t.Helper()
	writer := &fakePostingWriter{id: kernel.ID{0x42}}
	h, state, m, place := composerHandler(t, topic, func(s *Services) { s.Posting = testPosting(writer) })
	path := view.ConversationURL("acme", place.Channel, nil)
	if topic {
		path = view.ConversationURL("acme", place.Channel, &place.Topic)
	}
	return h, writer, state, m, place, path
}

// keyedSend posts values and returns the response; a plain success follows
// its 303 to the page the browser would show.
func keyedSend(t *testing.T, h http.Handler, path string, values url.Values, hx bool, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := composerRequest(h, path, values, hx)
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if w.Code == http.StatusSeeOther {
		if w.Header().Get("Location") != path {
			t.Fatalf("redirect to %q", w.Header().Get("Location"))
		}
		w = serveForm(h, "GET", path, "live", nil)
	}
	return w
}

func TestComposerIdempotencyKey(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			t.Run(fmt.Sprintf("topic=%t/htmx=%t", topic, hx), func(t *testing.T) {
				h, writer, state, m, place, path := keyedComposer(t, topic)
				defer state.Open(m.Organization.ID, m.Member.ID, place.Channel)()
				success := http.StatusSeeOther
				if hx {
					success = http.StatusOK
				}
				// Every render carries one fresh canonical key, older pages too.
				_, values := composerForm(t, serveForm(h, "GET", path, "live", nil))
				_, older := composerForm(t, serveForm(h, "GET", path+"?before=40", "live", nil))
				first := values.Get("idempotency_key")
				if len(values["idempotency_key"]) != 1 || !canonicalKey.MatchString(first) || !canonicalKey.MatchString(older.Get("idempotency_key")) || older.Get("idempotency_key") == first {
					t.Fatalf("rendered keys %q and %q", first, older.Get("idempotency_key"))
				}
				// A 422 keeps the submitted key exactly.
				values.Set("body", "\n\n")
				if _, kept := composerForm(t, keyedSend(t, h, path, values, hx, http.StatusUnprocessableEntity)); kept.Get("idempotency_key") != first {
					t.Fatal("422 replaced the submitted key")
				}
				// A success renders a fresh key.
				values.Set("body", "hello")
				_, next := composerForm(t, keyedSend(t, h, path, values, hx, success))
				if fresh := next.Get("idempotency_key"); !canonicalKey.MatchString(fresh) || fresh == first || writer.inserts != 1 {
					t.Fatalf("success rendered key %q after %q, inserts %d", fresh, first, writer.inserts)
				}
				// A repeat of the first send is answered as a success naming
				// the stored message, and writes nothing.
				next.Set("idempotency_key", first)
				next.Set("body", "hello")
				w := keyedSend(t, h, path, next, hx, success)
				if hx && !strings.Contains(w.Body.String(), `data-posted-message="`+view.MessageDOMID(kernel.ID{0x42})+`"`) || writer.inserts != 1 {
					t.Fatalf("repeat: inserts %d, %s", writer.inserts, w.Body.String())
				}
				// Another body under the used key: 409 with the draft kept, the
				// stored message named, a fresh key and the generation still open.
				_, reused := composerForm(t, w)
				generation := reused.Get("composer")
				reused.Set("idempotency_key", first)
				reused.Set("body", "changed")
				w = keyedSend(t, h, path, reused, hx, http.StatusConflict)
				_, conflict := composerForm(t, w)
				// An enhanced 409 leaves the draft in the page's textarea (#1173).
				draft := "changed"
				if hx {
					draft = ""
				}
				if conflict.Get("body") != draft || conflict.Get("composer") != generation || conflict.Get("idempotency_key") == first || !canonicalKey.MatchString(conflict.Get("idempotency_key")) || writer.inserts != 1 {
					t.Fatalf("409 controls %v, inserts %d", conflict, writer.inserts)
				}
				if !strings.Contains(w.Body.String(), `data-posted-message="`+view.MessageDOMID(kernel.ID{0x42})+`"`) || !strings.Contains(w.Body.String(), "A different message was already sent from this composer.") {
					t.Fatalf("409 does not name the stored message or explain: %s", w.Body.String())
				}
				if hx != strings.HasPrefix(w.Body.String(), composerFragment) {
					t.Fatal("409 must render the composer alone for htmx and the page otherwise")
				}
				if !state.Signal(m.Organization.ID, place, typing.Typist{Member: m.Member.ID}, generation, time.Now()) {
					t.Fatal("409 closed the submitted generation")
				}
				// Text that starts with the stored body, as after a lost answer:
				// htmx gets the body to remove instead of the error (#1179); a
				// plain form, or a stored message in another channel or with a
				// quote, keeps the error.
				for _, c := range []struct {
					channel kernel.ID
					quote   *conversation.Quote
				}{{place.Channel, nil}, {kernel.ID{9}, nil}, {place.Channel, &conversation.Quote{}}} {
					for k, stored := range writer.keyed {
						stored.ChannelID, stored.Quote = c.channel, c.quote
						writer.keyed[k] = stored
					}
					reused.Set("body", "hello again")
					body := keyedSend(t, h, path, reused, hx, http.StatusConflict).Body.String()
					recovered := hx && c.channel == place.Channel && c.quote == nil
					if strings.Contains(body, `data-sent-body="hello"`) != recovered || strings.Contains(body, "A different message was already sent") == recovered {
						t.Fatalf("409 in channel %v, quote %v: %s", c.channel, c.quote, body)
					}
				}
			})
		}
	}
}

func TestComposerKeyField(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			for _, tt := range []struct {
				name    string
				keys    []string
				status  int
				inserts int
			}{
				{"malformed", []string{"not-a-key"}, http.StatusBadRequest, 0},
				{"not canonical", []string{"0123456789abcdef0123456789abcdef"}, http.StatusBadRequest, 0},
				{"repeated", []string{"01234567-89ab-4cde-8f01-23456789abcd", "01234567-89ab-4cde-8f01-23456789abcd"}, http.StatusBadRequest, 0},
				// Pages rendered before keys existed still post, without deduplication.
				{"absent", nil, 0, 2},
			} {
				t.Run(fmt.Sprintf("topic=%t/htmx=%t/%s", topic, hx, tt.name), func(t *testing.T) {
					h, writer, _, _, _, path := keyedComposer(t, topic)
					status := tt.status
					if status == 0 {
						status = map[bool]int{false: http.StatusSeeOther, true: http.StatusOK}[hx]
					}
					for range 2 {
						values := url.Values{"body": {"hello"}, "cursor": {"0"}}
						values["idempotency_key"] = tt.keys
						if w := composerRequest(h, path, values, hx); w.Code != status {
							t.Fatalf("status %d, want %d", w.Code, status)
						}
					}
					if writer.inserts != tt.inserts {
						t.Fatalf("inserts %d, want %d", writer.inserts, tt.inserts)
					}
				})
			}
		}
	}
}

// After branching moved the stored message into another topic, a repeat's
// composer stays on the requested feed or topic, and so does typing.
func TestComposerRepeatAfterMove(t *testing.T) {
	for _, topic := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			t.Run(fmt.Sprintf("topic=%t/htmx=%t", topic, hx), func(t *testing.T) {
				success := http.StatusSeeOther
				if hx {
					success = http.StatusOK
				}
				h, writer, state, m, place, path := keyedComposer(t, topic)
				defer state.Open(m.Organization.ID, m.Member.ID, place.Channel)()
				_, values := composerForm(t, serveForm(h, "GET", path, "live", nil))
				key := values.Get("idempotency_key")
				values.Set("body", "hello")
				_, values = composerForm(t, keyedSend(t, h, path, values, hx, success))
				moved := typing.Place{Channel: place.Channel, Topic: kernel.ID{9}}
				for scope, stored := range writer.keyed {
					stored.TopicID = moved.Topic
					writer.keyed[scope] = stored
				}
				submitted := values.Get("composer")
				values.Set("idempotency_key", key)
				values.Set("body", "hello")
				// A plain repeat redirects to the requested path; keyedSend
				// follows it to the page the browser would show.
				w := keyedSend(t, h, path, values, hx, success)
				_, signal := enhancedForms(t, w.Body.String())
				typingURL := view.ConversationURL("acme", place.Channel, &place.Topic) + "/typing"
				// An enhanced answer keeps the page's form, so it has no action.
				action, fresh := composerForm(t, w)
				wantAction := path
				if hx {
					wantAction = ""
				}
				if action != wantAction || attr(signal, "hx-post") != typingURL || writer.inserts != 1 {
					t.Fatalf("repeat composer posts to %q, typing %q, inserts %d", action, attr(signal, "hx-post"), writer.inserts)
				}
				typist := typing.Typist{Member: m.Member.ID}
				if state.Signal(m.Organization.ID, place, typist, submitted, time.Now()) {
					t.Fatal("the repeat did not close the submitted generation in the requested place")
				}
				if state.Signal(m.Organization.ID, moved, typist, fresh.Get("composer"), time.Now()) || !state.Signal(m.Organization.ID, place, typist, fresh.Get("composer"), time.Now()) {
					t.Fatal("the new generation is not scoped to the requested place")
				}
			})
		}
	}
}

// The key never leaves the author's composer: neither the shared live item
// for the posted message nor its message.posted payload carries it.
func TestIdempotencyKeyStaysOutOfStreams(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	token, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	services := postgresServices(t, pool, sessions, "", false)
	h, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	key := conversation.NewIdempotencyKey().String()
	path := view.ChannelURL("acme", f.Channel.ID)
	if w := serveForm(h, "POST", path, token, url.Values{"body": {"hello"}, "idempotency_key": {key}}); w.Code != http.StatusSeeOther {
		t.Fatalf("post: %d", w.Code)
	}
	var seq int64
	var payload []byte
	if err := pool.QueryRow(t.Context(), "SELECT seq, data::text FROM event_log WHERE organization_id = $1 AND kind = 'message.posted'", f.OrganizationID).Scan(&seq, &payload); err != nil {
		t.Fatal(err)
	}
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	live, err := messageRenderer{messages: services.Messages, membership: m, renders: newRenderCache(t.Context())}.Render(t.Context(),
		realtime.Event{OrganizationID: f.OrganizationID, Seq: seq, Kind: conversation.KindPosted, ChannelID: f.Channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live.Data), "hello") {
		t.Fatalf("live item lacks the message: %s", live.Data)
	}
	hexKey := strings.ReplaceAll(key, "-", "")
	for name, data := range map[string]string{"payload": string(payload), "live item": string(live.Data)} {
		if strings.Contains(data, key) || strings.Contains(data, hexKey) || strings.Contains(data, "idempotency") {
			t.Fatalf("%s carries the key: %s", name, data)
		}
	}
}
