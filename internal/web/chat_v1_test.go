package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// A chat_v1 item is the same on two members' pages and in the stream (the
// shared render cache), shows only members of the URL's organisation, and
// leaves plain_text bodies as they were rendered before chat_v1.
func TestChatV1RenderAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	member := func(organization kernel.ID, handle, name string) (kernel.ID, kernel.ID) {
		account := identitytest.Account(t, pool, handle+"@example.org", name)
		return account, orgtest.Member(t, pool, organization, account, org.RoleMember, handle, 1)
	}
	bobAccount, bob := member(f.OrganizationID, "bob", "Bob Example")
	_, blank := member(f.OrganizationID, "blank", "\u3164")
	foreign := conversationtest.OrganizationWithOwner(t, pool, "globex", "general")
	_, spy := member(foreign.OrganizationID, "spy", "Foreign Spy")
	ref := func(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }

	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	posting := conversationpg.NewPosting(pool, postingSequence, postingEvents, nil)
	plain, err := posting.Post(ctx, m, f.Channel.ID, "**not bold** "+ref(bob)+" https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	rich, err := posting.Post(ctx, m, f.Channel.ID, "**Hi** "+ref(bob)+" and "+ref(blank)+", _see_ https://example.com/a.\n> from "+ref(spy)+
		" `x<y`\n```go\nfmt.Print(\"<b>\")\n```\nunknown "+ref(kernel.ID{0xee}))
	if err != nil {
		t.Fatal(err)
	}
	// Posting writes plain_text until #1203, so the fixture marks the row chat_v1.
	if _, err := pool.Exec(ctx, "UPDATE message SET format = 'chat_v1' WHERE id = $1", rich.ID); err != nil {
		t.Fatal(err)
	}

	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	handler, err := NewHandler("", catalogues, postgresServices(t, pool, sessions, "", false))
	if err != nil {
		t.Fatal(err)
	}
	// item returns the message's <li> as html renders it, without the
	// announcement only live items carry.
	item := func(page []byte, id kernel.ID) string {
		t.Helper()
		doc, err := html.Parse(bytes.NewReader(page))
		if err != nil {
			t.Fatal(err)
		}
		for n := range doc.Descendants() {
			if attr(n, "id") == view.MessageDOMID(id) {
				n.Attr = slices.DeleteFunc(n.Attr, func(a html.Attribute) bool { return a.Key == "data-announcement" })
				var b bytes.Buffer
				if err := html.Render(&b, n); err != nil {
					t.Fatal(err)
				}
				return b.String()
			}
		}
		t.Fatalf("no item %s", view.MessageDOMID(id))
		return ""
	}
	page := func(account kernel.ID) []byte {
		t.Helper()
		cookie, _, err := sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		return serveForm(handler, http.MethodGet, view.ChannelURL("acme", f.Channel.ID), cookie, nil).Body.Bytes()
	}
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, emptyPageCountsIn)
	stream := func(lang string) []byte {
		t.Helper()
		var langCtx context.Context
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", lang)
		catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { langCtx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
		event := eventOf(t, f.OrganizationID, rich.EventSeq, conversation.KindPosted, conversation.Moved{ChannelID: f.Channel.ID, ToTopicID: rich.TopicID})
		out, err := messageRenderer{messages: reader, membership: m, renders: newRenderCache(ctx)}.Render(langCtx, event)
		if err != nil {
			t.Fatal(err)
		}
		return out.Data
	}

	ownerPage := page(f.AccountID)
	got := item(ownerPage, rich.ID)
	if other := item(page(bobAccount), rich.ID); other != got {
		t.Fatalf("two members' pages differ:\n%s\n%s", got, other)
	}
	if live := item(stream("en"), rich.ID); live != got {
		t.Fatalf("stream differs from the page:\n%s\n%s", live, got)
	}
	for _, want := range []string{
		`<strong dir="auto">Hi</strong>`,
		`<bdi class="font-semibold text-fg">Bob Example</bdi> <span class="text-muted">@bob</span>`,
		`<bdi class="font-semibold text-fg">@blank</bdi></bdi>`,
		`<em dir="auto">see</em> <a class="text-brand underline wrap-anywhere" dir="auto" href="https://example.com/a" rel="noopener noreferrer">https://example.com/a</a>.`,
		`<blockquote dir="auto"`,
		`<code class="rounded-sm bg-hover px-1 font-mono text-body" dir="auto">x&lt;y</code>`,
		`<pre dir="auto" class="overflow-x-auto rounded-md bg-hover p-3 font-mono text-body"><code>fmt.Print(&#34;&lt;b&gt;&#34;)</code></pre>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("item lacks %s", want)
		}
	}
	if strings.Count(got, "@unknown member") != 2 || strings.Contains(got, realtime.FormatPayloadID(spy)) || strings.Contains(got, realtime.FormatPayloadID(kernel.ID{0xee})) {
		t.Errorf("unresolved references must show the placeholder, never the UUID:\n%s", got)
	}
	if strings.Contains(string(ownerPage), "Foreign Spy") || strings.Contains(string(ownerPage), "@spy") {
		t.Error("the page shows another organisation's member")
	}
	if ja := item(stream("ja"), rich.ID); strings.Count(ja, "@不明なメンバー") != 2 {
		t.Errorf("Japanese placeholder:\n%s", ja)
	}
	wantPlain := `<p id="body-` + view.MessageDOMID(plain.ID) + `" dir="auto" class="whitespace-pre-wrap break-words text-title">**not bold** &lt;@` + realtime.FormatPayloadID(bob) + `&gt; https://example.com</p>`
	if !strings.Contains(string(ownerPage), wantPlain) {
		t.Errorf("plain_text body changed:\n%s", item(ownerPage, plain.ID))
	}
}
