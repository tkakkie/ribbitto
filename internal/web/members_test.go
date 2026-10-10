package web

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestMembersPageAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	local := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	foreign := conversationtest.OrganizationWithOwner(t, pool, "globex", "foreign-channel")
	for i := range conversation.MemberPageSize + 2 {
		name := "Shared name"
		if i == 0 {
			name = "\u3164"
		}
		if i == 1 {
			name = "مريم <b>"
		}
		account := identitytest.Account(t, pool, fmt.Sprintf("member%d@example.org", i), name)
		orgtest.Member(t, pool, local.OrganizationID, account, org.RoleMember, fmt.Sprintf("person%d", i), 1)
	}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	cookie, _, err := sessions.Create(t.Context(), local.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, postgresServices(t, pool, sessions, "", false))
	if err != nil {
		t.Fatal(err)
	}
	path := view.ChannelURL("acme", local.Channel.ID) + "/members"
	seen := map[string]bool{}
	for pageNo := range 2 {
		w := serveForm(handler, "GET", path, cookie, nil)
		if w.Code != 200 {
			t.Fatalf("page: %d %s", w.Code, w.Body.String())
		}
		doc, err := html.Parse(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		count, next, back := 0, "", false
		for n := range doc.Descendants() {
			if n.DataAtom == atom.Li && n.Parent != nil && attr(n.Parent, "id") == "members-list" {
				value := text(n)
				if seen[value] {
					t.Fatalf("duplicate member %q", value)
				}
				seen[value] = true
				count++
			}
			if n.DataAtom == atom.A {
				href := attr(n, "href")
				if strings.Contains(href, "/members?after=") {
					next = href
				}
				if href == view.ChannelURL("acme", local.Channel.ID) {
					back = true
				}
			}
			if attr(n, "id") == "organization-stream" {
				stream, err := url.Parse(attr(n, "sse-connect"))
				if err != nil {
					t.Fatal(err)
				}
				q := stream.Query()
				if stream.Path != "/organizations/acme/events" || q.Get("want") != "sidebar" || q.Get("channel") != strings.TrimPrefix(view.ChannelURL("acme", local.Channel.ID), "/organizations/acme/channels/") || q.Get("after") != "1" || q.Has("topic") {
					t.Fatalf("stream scope: %v", stream)
				}
			}
		}
		want := conversation.MemberPageSize
		if pageNo == 1 {
			want = 3
		}
		if count != want || !back || (pageNo == 0) != (next != "") {
			t.Fatalf("count=%d next=%q back=%t", count, next, back)
		}
		if strings.Contains(w.Body.String(), "foreign-channel") {
			t.Fatal("out-of-page member or foreign sidebar")
		}
		path = next
	}
	if len(seen) != conversation.MemberPageSize+3 || !seen["@person0"] || !seen["مريم <b> @person1"] {
		t.Fatalf("names or paging lost members: %v", seen)
	}
	// Each negative case changes one scope while keeping the other fixed.
	reader := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor)
	for _, tt := range []struct {
		name                  string
		organization, channel kernel.ID
	}{
		{"organisation", foreign.OrganizationID, local.Channel.ID},
		{"channel", local.OrganizationID, kernel.ID{0xee}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := reader.Members(t.Context(), org.Membership{Organization: org.Organization{ID: tt.organization}}, tt.channel, nil); !errors.Is(err, conversation.ErrChannelNotFound) {
				t.Fatalf("scope: %v", err)
			}
		})
	}
	for _, suffix := range []string{"?after=bad", "?after=%zz", "?after=01000000-0000-0000-0000-000000000000&after=01000000-0000-0000-0000-000000000000"} {
		w := serveForm(handler, "GET", view.ChannelURL("acme", local.Channel.ID)+"/members"+suffix, cookie, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor: %d", w.Code)
		}
	}
	// Follow the channel header's ordinary link without JavaScript.
	w := serveForm(handler, "GET", view.ChannelURL("acme", local.Channel.ID), cookie, nil)
	if !strings.Contains(w.Body.String(), `href="`+view.ChannelURL("acme", local.Channel.ID)+`/members"`) {
		t.Fatal("missing channel header link")
	}
	// The page's strings resolve in both catalogues instead of falling back to
	// their message IDs.
	for _, tt := range []struct{ lang, title, back string }{
		{"en", "Channel members", "Back to channel"},
		{"ja", "チャンネルのメンバー", "チャンネルに戻る"},
	} {
		r := httptest.NewRequest(http.MethodGet, view.ChannelURL("acme", local.Channel.ID)+"/members", nil)
		r.Header.Set("Accept-Language", tt.lang)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		if !strings.Contains(body, tt.title) || !strings.Contains(body, tt.back) || strings.Contains(body, "people.") {
			t.Errorf("%s: members page strings did not resolve", tt.lang)
		}
	}
}
