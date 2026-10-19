package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type fakeMemberSearch struct {
	membership org.Membership
	query      string
	calls      int
	err        error
	empty      bool
}

func (s *fakeMemberSearch) Search(_ context.Context, m org.Membership, q string) ([]org.MemberCandidate, error) {
	s.membership, s.query = m, q
	s.calls++
	if s.empty || s.err != nil {
		return nil, s.err
	}
	return []org.MemberCandidate{{ID: kernel.ID{2}, Handle: "zoe", DisplayName: "Zoe <script>"}, {ID: kernel.ID{1}, Handle: "alice", DisplayName: "Alice"}}, nil
}

func TestMentionCandidates(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	search := &fakeMemberSearch{}
	s := testServices(asAlice)
	s.MemberSearch = search
	now := time.Unix(1, 0)
	s.MemberLimits = memberLimitOverride(s.MemberLimits, func(l *MemberLimits) {
		l.mention = middleware.NewMemberRateLimiterWithBudget(func() time.Time { return now }, 4, 8, 2)
	})
	h, err := NewHandler("", catalogues, s)
	if err != nil {
		t.Fatal(err)
	}
	query := " @<\"& "
	for _, tc := range []struct {
		lang, count string
		empty       bool
		failure     error
		status      int
	}{
		{"en", "Members found: 2", false, nil, 200},
		{"ja", "2 人のメンバーが見つかりました。", false, nil, 200},
		{"en", "No members found.", true, nil, 200},
		{"ja", "該当するメンバーはいません。", true, nil, 200},
		{"en", "", false, errors.New("search unavailable"), 500},
	} {
		search.empty, search.err = tc.empty, tc.failure
		r := httptest.NewRequest("GET", "/organizations/acme/mention-candidates?q="+url.QueryEscape(query), nil)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
		r.Header.Set("Accept-Language", tc.lang)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if search.query != query || search.membership.Organization.Slug != "acme" || search.membership.Member.Handle != "alice" {
			t.Fatalf("search received %q, %+v", search.query, search.membership)
		}
		if tc.failure != nil {
			continue
		}
		raw := w.Body.String()
		if !strings.HasPrefix(raw, `<ul id="mention-listbox"`) || !strings.HasSuffix(raw, "</ul>") {
			t.Fatalf("fragment root must be the listbox: %s", raw)
		}
		doc, err := html.Parse(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		list := find(doc, atom.Ul)
		if list == nil || attr(list, "id") != "mention-listbox" || attr(list, "role") != "listbox" || attr(list, "aria-label") == "" || attr(list, "data-query") != query || attr(list, "data-count") != tc.count {
			t.Fatalf("invalid listbox: %+v", list)
		}
		options := 0
		for n := range list.Descendants() {
			if n.DataAtom != atom.Li {
				continue
			}
			want := []org.MemberCandidate{{ID: kernel.ID{2}, Handle: "zoe", DisplayName: "Zoe <script>"}, {ID: kernel.ID{1}, Handle: "alice", DisplayName: "Alice"}}[options]
			if attr(n, "role") != "option" || attr(n, "id") != fmt.Sprintf("mention-option-%x", want.ID) || attr(n, "data-member-id") != realtime.FormatPayloadID(want.ID) || attr(n, "data-handle") != want.Handle || text(n) != want.DisplayName+" @"+want.Handle {
				t.Fatalf("invalid option: %+v", n)
			}
			options++
		}
		if options != map[bool]int{true: 0, false: 2}[tc.empty] || find(doc, atom.Script) != nil {
			t.Fatal("wrong options or unescaped markup")
		}
	}
	search.err = nil
	for range 3 {
		serveForm(h, "GET", "/organizations/acme/mention-candidates", "live", nil)
	}
	calls := search.calls
	w := serveForm(h, "GET", "/organizations/acme/mention-candidates", "live", nil)
	if w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Retry-After") != "1" || search.calls != calls {
		t.Fatalf("rate limit: %d %s", w.Code, w.Body.String())
	}
	now = now.Add(time.Second)
	if w := serveForm(h, "GET", "/organizations/acme/mention-candidates", "live", nil); w.Code != 200 {
		t.Fatalf("refill: %d", w.Code)
	}
}

func TestMentionCandidatesOrganizationScopeAgainstPostgreSQL(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	other := orgtest.Organization(t, pool, "other", "Other", 0)
	localName, foreignName, unjoinedName := "Local all everyone here channel", "Foreign all everyone here channel", "Unjoined all everyone here channel"
	local := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "local@example.org", localName), org.RoleMember, "local", 1)
	foreignAccount := identitytest.Account(t, pool, "foreign@example.org", foreignName)
	foreign := orgtest.Member(t, pool, other, foreignAccount, org.RoleMember, "foreign", 1)
	unjoined := identitytest.Account(t, pool, "unjoined@example.org", unjoinedName)
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	cookie, _, err := sessions.Create(t.Context(), f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler("", catalogues, postgresServices(t, pool, sessions, "", false))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"all", "everyone", "here", "channel"} {
		w := serveForm(h, "GET", "/organizations/acme/mention-candidates?q="+q, cookie, nil)
		body := w.Body.String()
		if w.Code != 200 || strings.Count(body, `role="option"`) != 1 || !strings.Contains(body, realtime.FormatPayloadID(local)) || !strings.Contains(body, localName) {
			t.Fatalf("%s: %d %s", q, w.Code, body)
		}
		for _, forbidden := range []string{realtime.FormatPayloadID(foreign), realtime.FormatPayloadID(unjoined), foreignName, unjoinedName, "@foreign"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s exposed %q", q, forbidden)
			}
		}
	}
	outsider, _, err := sessions.Create(t.Context(), foreignAccount)
	if err != nil {
		t.Fatal(err)
	}
	if w := serveForm(h, "GET", "/organizations/acme/mention-candidates?q=all", outsider, nil); w.Code != 404 {
		t.Fatalf("non-member: %d %s", w.Code, w.Body.String())
	}
}
