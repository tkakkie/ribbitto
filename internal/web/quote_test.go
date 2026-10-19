package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// A quote is posted through the composer's handler and shown the same on
// the page and in the shared stream render. A quoted message outside the
// channel is one 404 that writes nothing and shows none of its text.
func TestQuoteAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	random := conversationtest.Channel(t, pool, f.OrganizationID, "random", false)
	bob := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob Example"), org.RoleMember, "bob", 1)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	acme := org.Organization{ID: f.OrganizationID, Slug: "acme"}
	m := org.Membership{Organization: acme, Member: org.Member{ID: f.MemberID, OrganizationID: f.OrganizationID}}
	posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	post := func(m org.Membership, channel kernel.ID, body string) conversation.Message {
		t.Helper()
		posted, err := posting.Post(ctx, m, channel, body)
		if err != nil {
			t.Fatal(err)
		}
		return posted
	}
	original := post(org.Membership{Organization: acme, Member: org.Member{ID: bob}}, f.Channel.ID, "**Deploy** <b>today</b>")
	outside := []kernel.ID{{0xee}, post(m, random.ID, "random secret").ID,
		post(org.Membership{Organization: org.Organization{ID: foreign.OrganizationID}, Member: org.Member{ID: foreign.MemberID}}, foreign.Channel.ID, "globex secret").ID}

	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	services := postgresServices(t, pool, sessions, "", false)
	services.PostLimits = middleware.NewMemberRateLimiterWithBudget(time.Now, 1000, 1000, 4)
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := sessions.Create(ctx, f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	feed := view.ConversationURL("acme", f.Channel.ID, nil)
	send := func(quote ...string) *httptest.ResponseRecorder {
		return serveForm(handler, http.MethodPost, feed, cookie, url.Values{"body": {"Agreed"}, "quote": quote})
	}
	var latest kernel.ID
	var seq int64
	state := func() string {
		t.Helper()
		if err := pool.QueryRow(ctx, "SELECT id, event_seq FROM message WHERE organization_id = $1 ORDER BY event_seq DESC LIMIT 1", f.OrganizationID).Scan(&latest, &seq); err != nil {
			t.Fatal(err)
		}
		return view.MessageDOMID(latest)
	}

	events := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_log WHERE organization_id = $1", f.OrganizationID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	beforeEvents := events()
	before := state()
	id := realtime.FormatPayloadID(original.ID)
	for _, quote := range [][]string{{"not-a-uuid"}, {strings.ToUpper(id)}, {id, id}} {
		if w := send(quote...); w.Code != http.StatusBadRequest {
			t.Errorf("quote %q: status %d", quote, w.Code)
		}
	}
	chip := func(method string, quoted ...string) *httptest.ResponseRecorder {
		form := url.Values{"quote": quoted}
		path := feed + "/quote"
		if method == http.MethodGet {
			path = feed + "?" + form.Encode()
		}
		return serveForm(handler, method, path, cookie, form)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, raw := range [][]string{{"bad"}, {strings.ToUpper(id)}, {id, id}} {
			if w := chip(method, raw...); w.Code != 400 {
				t.Fatalf("%s malformed chip: %d %s", method, w.Code, w.Body.String())
			}
		}
		for _, quoted := range outside {
			w := chip(method, realtime.FormatPayloadID(quoted))
			if w.Code != 404 || w.Body.String() != "404 page not found\n" {
				t.Fatalf("%s out-of-scope chip: %d %s", method, w.Code, w.Body.String())
			}
			form := url.Values{"quote": {realtime.FormatPayloadID(quoted)}, "quote_text": {"absent"}}
			path := feed + "/quote"
			if method == "GET" {
				path = feed + "?" + form.Encode()
			}
			w = serveForm(handler, method, path, cookie, form)
			if w.Code != 404 || w.Body.String() != "404 page not found\n" {
				t.Fatalf("%s scoped part: %d %s", method, w.Code, w.Body.String())
			}
		}
		w := chip(method, id)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Deploy &lt;b&gt;today&lt;/b&gt;") || !strings.Contains(w.Body.String(), "Bob Example") || !strings.Contains(w.Body.String(), `name="quote" value="`+id+`"`) {
			t.Fatalf("%s chip: %d %s", method, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `name="quote_text"`) {
			t.Fatal("whole chip contains a part control")
		}
		if method == http.MethodPost && strings.Contains(w.Body.String(), "message-composer-state") {
			t.Fatal("chip route returned composer state")
		}
	}
	// Isolate organisation scope from channel scope: the foreign message and
	// channel match, but the membership still belongs to acme.
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, emptyPageCountsIn)
	w := httptest.NewRecorder()
	if ok := (channelPages{messages: reader}).readQuote(w, httptest.NewRequest("GET", "/", nil), m, foreign.Channel.ID, url.Values{"quote": {realtime.FormatPayloadID(outside[2])}, "quote_text": {"absent"}}, &view.ChannelPage{}); ok || w.Code != 404 || w.Body.String() != "404 page not found\n" {
		t.Fatalf("organisation-only scope: %d %s", w.Code, w.Body.String())
	}
	compose := func(body string, hx bool, part ...string) *httptest.ResponseRecorder {
		form := url.Values{"body": {body}, "quote": {id}}
		if part != nil {
			form["quote_text"] = part
		}
		r := httptest.NewRequest("POST", feed, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		if hx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertPartAutofocus := func(w *httptest.ResponseRecorder, want bool) {
		t.Helper()
		doc, err := html.Parse(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		for n := range doc.Descendants() {
			if attr(n, "id") == "quote-text" {
				got := slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return a.Key == "autofocus" })
				if got != want {
					t.Fatalf("part autofocus = %t, want %t (status %d)", got, want, w.Code)
				}
				return
			}
		}
		t.Fatal("missing part editor")
	}
	for _, hx := range []bool{false, true} {
		if w := compose("", hx); w.Code != 422 || !strings.Contains(w.Body.String(), `name="quote" value="`+id+`"`) || strings.Contains(w.Body.String(), `name="quote_text"`) {
			t.Fatalf("422 chip (htmx %t): %d %s", hx, w.Code, w.Body.String())
		}
		for _, part := range []string{"", "absent", "Deploy"} {
			body := "part answer"
			if part == "Deploy" {
				body = ""
			}
			w := compose(body, hx, part)
			if w.Code != 422 || !strings.Contains(w.Body.String(), `name="quote_text"`) || !strings.Contains(w.Body.String(), part+"</textarea>") {
				t.Fatalf("422 part %q (htmx %t): %d %s", part, hx, w.Code, w.Body.String())
			}
			assertPartAutofocus(w, false)
			if body != "" {
				doc, err := html.Parse(strings.NewReader(w.Body.String()))
				if err != nil {
					t.Fatal(err)
				}
				for n := range doc.Descendants() {
					if attr(n, "id") == "message-body" {
						if attr(n, "aria-invalid") != "false" {
							t.Fatal("valid body marked invalid")
						}
					}
					if attr(n, "id") == "quote-text" && attr(n, "aria-invalid") != "true" {
						t.Fatal("invalid part not marked invalid")
					}
				}
				if strings.Contains(w.Body.String(), "Enter 1–4000 characters without control characters or directional embeddings and overrides.") || strings.Contains(w.Body.String(), `data-invalid`) {
					t.Fatal("quote-only error marked the body invalid")
				}
			}
		}
	}
	if w := serveForm(handler, "POST", feed+"/quote", cookie, nil); w.Code != 200 || strings.Contains(w.Body.String(), `name="quote"`) {
		t.Fatalf("remove chip: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range []struct {
			form   url.Values
			status int
		}{
			{url.Values{"part": {id}}, 200},
			{url.Values{"part": {id}, "quote_text": {""}}, 422},
			{url.Values{"quote": {id}, "quote_text": {"Deploy"}}, 200},
			{url.Values{"quote": {id}, "quote_text": {""}}, 422},
			{url.Values{"quote": {id}, "quote_text": {"absent"}}, 422},
			{url.Values{"quote": {id}, "quote_text": {"**Deploy**"}}, 422},
		} {
			path := feed + "/quote"
			if method == "GET" {
				path = feed + "?" + tc.form.Encode()
			}
			w := serveForm(handler, method, path, cookie, tc.form)
			if method == "GET" {
				if w.Code != 200 || strings.Contains(w.Body.String(), `name="quote_text"`) != tc.form.Has("part") || tc.form.Has("part") && !strings.Contains(w.Body.String(), "Deploy &lt;b&gt;today&lt;/b&gt;</textarea>") {
					t.Fatalf("GET must ignore URL quote_text: %d %s", w.Code, w.Body.String())
				}
				if tc.form.Has("part") {
					assertPartAutofocus(w, true)
				}
				continue
			}
			assertPartAutofocus(w, tc.status == http.StatusOK)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `name="quote_text"`) || !strings.Contains(w.Body.String(), tc.form.Get("quote_text")) || !strings.Contains(w.Body.String(), `name="quote" value="`+id+`"`) {
				t.Fatalf("%s %v: %d %s", method, tc.form, w.Code, w.Body.String())
			}
		}
	}
	for _, quoted := range outside {
		for _, part := range []string{"", "absent"} {
			form := url.Values{"body": {"part answer"}, "quote": {realtime.FormatPayloadID(quoted)}, "quote_text": {part}}
			if w := serveForm(handler, "POST", feed, cookie, form); w.Code != 404 || w.Body.String() != "404 page not found\n" {
				t.Fatalf("part %q scope %x: %d %s", part, quoted, w.Code, w.Body.String())
			}
		}
	}
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range []struct {
			form   url.Values
			status int
		}{
			{url.Values{"part": {realtime.FormatPayloadID(outside[2])}, "quote_text": {"absent"}}, 404},
			{url.Values{"quote": {realtime.FormatPayloadID(outside[2])}, "part": {id}}, 400},
		} {
			path := feed + "/quote"
			if method == "GET" {
				path = feed + "?" + tc.form.Encode()
			}
			if w := serveForm(handler, method, path, cookie, tc.form); w.Code != tc.status {
				t.Fatalf("%s part scope: %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	if w := serveForm(handler, "GET", feed+"?quote="+id+"&quote_text=secret", cookie, nil); w.Code != 200 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), `name="quote_text"`) || !strings.Contains(w.Body.String(), `name="quote" value="`+id+`"`) {
		t.Fatalf("GET leaked URL quote_text: %d %s", w.Code, w.Body.String())
	}
	form := url.Values{"body": {""}, "quote": {realtime.FormatPayloadID(outside[2])}, "part": {id}}
	if w := serveForm(handler, "POST", feed, cookie, form); w.Code != 404 {
		t.Fatalf("POST part overrode quote: %d %s", w.Code, w.Body.String())
	}
	var answers []string
	for _, quoted := range outside {
		w := send(realtime.FormatPayloadID(quoted))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("quote %x: status %d: %s", quoted, w.Code, w.Body.String())
		}
		answers = append(answers, w.Body.String())
	}
	if len(slices.Compact(answers)) != 1 {
		t.Errorf("the answers differ: %q", answers)
	}
	if after := state(); after != before || events() != beforeEvents {
		t.Fatal("a refused quote was posted")
	}
	if w := send(id); w.Code != http.StatusSeeOther {
		t.Fatalf("quote: status %d: %s", w.Code, w.Body.String())
	}
	var quotedMessage, quotedMember kernel.ID
	var text string
	itemID := state()
	if err := pool.QueryRow(ctx, "SELECT quoted_message_id, quoted_member_id, quote_text FROM message WHERE id = $1", latest).Scan(&quotedMessage, &quotedMember, &text); err != nil ||
		quotedMessage != original.ID || quotedMember != bob || text != "Deploy <b>today</b>" {
		t.Fatalf("stored %x, %x, %q, %v", quotedMessage, quotedMember, text, err)
	}

	// The item as html renders it; the quote, the <figure>, is its first.
	find := func(page []byte, id string) (item, quote *html.Node) {
		t.Helper()
		doc, err := html.Parse(bytes.NewReader(page))
		if err != nil {
			t.Fatal(err)
		}
		for n := range doc.Descendants() {
			if attr(n, "id") == id {
				item = n
			}
			if item != nil && n.Data == "figure" {
				return item, n
			}
		}
		t.Fatalf("no quote in %s", id)
		return nil, nil
	}
	rendered := func(n *html.Node) string {
		var b bytes.Buffer
		if err := html.Render(&b, n); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	pageOf := func() []byte { return serveForm(handler, http.MethodGet, feed, cookie, nil).Body.Bytes() }
	var english context.Context
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", "en")
	catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { english = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	event := eventOf(t, f.OrganizationID, seq, conversation.KindPosted, conversation.Moved{ChannelID: f.Channel.ID, ToTopicID: f.Channel.DefaultTopicID})
	live, err := messageRenderer{messages: reader, membership: m, renders: newRenderCache(ctx)}.Render(english, event)
	if err != nil {
		t.Fatal(err)
	}

	pageItem, pageQuote := find(pageOf(), itemID)
	liveItem, liveQuote := find(live.Data, itemID)
	if got, want := rendered(liveQuote), rendered(pageQuote); got != want {
		t.Fatalf("live quote differs from the page's:\n%s\n%s", got, want)
	}
	if _, ok := attrOK(pageItem, "data-announcement"); ok {
		t.Error("the page item announces")
	}
	if got := attr(liveItem, "data-announcement"); got != "New ribbit @owner: Agreed" {
		t.Errorf("announcement %q, want the quoting message's own body only", got)
	}
	liveItem.Attr = slices.DeleteFunc(liveItem.Attr, func(a html.Attribute) bool { return a.Key == "data-announcement" })
	if got, want := rendered(liveItem), rendered(pageItem); got != want {
		t.Fatalf("live item differs from the page's:\n%s\n%s", got, want)
	}
	quote := rendered(pageQuote)
	for _, want := range []string{
		`<bdi class="font-semibold text-fg">Bob Example</bdi> <span class="text-muted">@bob</span>`,
		`href="` + view.MessageURL("acme", original.ID) + `"`,
		`<blockquote dir="auto" class="m-0 whitespace-pre-wrap break-words text-body text-muted">Deploy &lt;b&gt;today&lt;/b&gt;</blockquote>`,
	} {
		if !strings.Contains(quote, want) {
			t.Errorf("quote lacks %s:\n%s", want, quote)
		}
	}

	if w := compose("Enhanced answer", true); w.Code != 200 || !strings.Contains(w.Body.String(), `id="composer-quote" hidden`) || strings.Contains(w.Body.String(), `name="quote"`) {
		t.Fatalf("success retained the chip: %d %s", w.Code, w.Body.String())
	}

	partForm := url.Values{"body": {"part answer @owner"}, "quote": {id}, "quote_text": {"  <b>today</b>\r\n"}, "idempotency_key": {*freshKey()}}
	if w := serveForm(handler, "POST", feed, cookie, partForm); w.Code != 303 {
		t.Fatalf("part send: %d %s", w.Code, w.Body.String())
	}
	state()
	if err := pool.QueryRow(ctx, "SELECT quote_text FROM message WHERE id = $1", latest).Scan(&text); err != nil || text != "<b>today</b>" {
		t.Fatalf("stored part %q: %v", text, err)
	}

	for _, tc := range []struct {
		part   string
		status int
	}{{"<b>today</b>", 303}, {"Deploy", 409}, {"", 422}} {
		partForm.Set("quote_text", tc.part)
		if w := serveForm(handler, "POST", feed, cookie, partForm); w.Code != tc.status {
			t.Fatalf("part retry %q: %d %s", tc.part, w.Code, w.Body.String())
		}
	}

	for _, body := range []string{"retry answer", "retry answer @owner"} {
		for _, tc := range []struct {
			name        string
			first, next *string
			foreign     bool
			status      int
		}{
			{"whole to part", nil, new("Deploy"), false, 409},
			{"part to whole", new("Deploy"), nil, false, 409},
			{"whole to equal part", nil, new("Deploy <b>today</b>"), false, 303},
			{"equal part to whole", new("Deploy <b>today</b>"), nil, false, 303},
			{"scope before comparison", nil, new("absent"), true, 404},
			{"substring before comparison", nil, new("absent"), false, 422},
		} {
			t.Run(body+"/"+tc.name, func(t *testing.T) {
				key := *freshKey()
				form := url.Values{"body": {body}, "quote": {id}, "idempotency_key": {key}}
				if tc.first != nil {
					form.Set("quote_text", *tc.first)
				}
				if w := serveForm(handler, "POST", feed, cookie, form); w.Code != 303 {
					t.Fatalf("first send: %d %s", w.Code, w.Body.String())
				}
				form.Del("quote_text")
				if tc.next != nil {
					form.Set("quote_text", *tc.next)
				}
				if tc.foreign {
					form.Set("quote", realtime.FormatPayloadID(outside[2]))
				}
				for _, hx := range []bool{false, true} {
					r := httptest.NewRequest("POST", feed, strings.NewReader(form.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
					if hx {
						r.Header.Set("HX-Request", "true")
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					status := tc.status
					if hx && status == 303 {
						status = 200
					}
					if w.Code != status {
						t.Fatalf("retry (htmx %t): %d, want %d: %s", hx, w.Code, status, w.Body.String())
					}
					if tc.status == 422 && !strings.Contains(w.Body.String(), `name="idempotency_key" value="`+key+`"`) {
						t.Fatal("422 replaced the submitted key")
					}
					if tc.status == 404 && w.Body.String() != "404 page not found\n" {
						t.Fatal("404 returned composer state or foreign text")
					}
				}
			})
		}
	}

	// Deleting the original keeps the author and the text, without a link.
	if _, err := pool.Exec(ctx, "DELETE FROM message WHERE id = $1", original.ID); err != nil {
		t.Fatal(err)
	}
	_, pageQuote = find(pageOf(), itemID)
	if quote := rendered(pageQuote); strings.Contains(quote, "<a ") || !strings.Contains(quote, "Bob Example") || !strings.Contains(quote, "Deploy &lt;b&gt;today") {
		t.Errorf("quote of a deleted original:\n%s", quote)
	}
}

// An ambiguous chip selection is rejected before any message read.
func TestReadQuoteAmbiguousInput(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		values := url.Values{"quote": {realtime.FormatPayloadID(kernel.ID{9})}, "part": {realtime.FormatPayloadID(kernel.ID{1})}}
		if (channelPages{}).readQuote(w, httptest.NewRequest(method, "/", nil), org.Membership{}, kernel.ID{2}, values, &view.ChannelPage{}) || w.Code != 400 {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
}

func TestQuoteInvalidPreservesPostStatus(t *testing.T) {
	p := channelPages{messages: fakeMessages{}, pages: &pageRenderer{stylesheetURL: func() (string, error) { return "", nil }}}
	r := httptest.NewRequest("POST", "/", nil)
	w := httptest.NewRecorder()
	p.render(w, r, org.Membership{}, kernel.ID{1}, 409, view.ChannelPage{QuoteInvalid: true, BodyError: "message.error.repeat"}, nil)
	if w.Code != 409 {
		t.Fatalf("invalid part overwrote POST status: %d", w.Code)
	}
}
