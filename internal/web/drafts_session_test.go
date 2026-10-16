package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// draftsLogin is one signed-in session; the member's ID is the account's.
type draftsLogin struct {
	member, session string
	expires         time.Time
}

// draftsLogins resolves a session token to its login, as the session
// middleware would, and makes each account a member of acme.
type draftsLogins map[string]*draftsLogin

func (l draftsLogins) Resolve(_ context.Context, token string) (identity.Account, identity.Session, error) {
	login, ok := l[token]
	if !ok {
		return identity.Account{}, identity.Session{}, identity.ErrNoSession
	}
	account, _ := pathID(login.member)
	session, _ := pathID(login.session)
	return identity.Account{ID: account, DisplayName: "Member"}, identity.Session{ID: session, ExpiresAt: login.expires}, nil
}

func (draftsLogins) Member(_ context.Context, account *identity.Account, slug string) (org.Membership, error) {
	if account == nil || slug != "acme" {
		return org.Membership{}, org.ErrNotFound
	}
	return org.Membership{Organization: org.Organization{Slug: "acme", Name: "Acme"}, Member: org.Member{ID: account.ID, Role: org.RoleMember, Handle: "member"}}, nil
}

func (draftsLogins) HomeSlug(context.Context, *identity.Account) (string, error) { return "acme", nil }

// draftsToken is a token in the real encoding, so its hash can be checked.
func draftsToken(b byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// TestPagesCarryOwnSession checks the drafts script's session marker: each
// organisation page carries its own request's member, session, expiry and
// render time, never another member's or session's, and never the token or
// its hash (decision 37 (d)).
func TestPagesCarryOwnSession(t *testing.T) {
	expires := time.Now().Add(30 * 24 * time.Hour)
	logins := draftsLogins{
		draftsToken(1): {"0190a000-0000-7000-8000-00000000000a", "019a1b2c-3d4e-7000-8000-0000000000a1", expires},
		draftsToken(2): {"0190a000-0000-7000-8000-00000000000a", "019a1b2c-3d4f-7000-8000-0000000000a2", expires.Add(time.Minute)},
		draftsToken(3): {"0190a000-0000-7000-8000-00000000000b", "019a1b2c-3d50-7000-8000-0000000000b1", expires.Add(time.Hour)},
	}
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, testServices(func(s *Services) { s.Sessions, s.Authz = logins, logins }))
	if err != nil {
		t.Fatal(err)
	}
	for token, login := range logins {
		raw, _ := base64.RawURLEncoding.DecodeString(token)
		hash := sha256.Sum256(raw)
		for _, path := range []string{view.ChannelURL("acme", kernel.ID{1}), "/organizations/acme/channels"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
			w := httptest.NewRecorder()
			before := time.Now().UnixMilli()
			handler.ServeHTTP(w, r)
			after := time.Now().UnixMilli()
			body := w.Body.String()
			doc, err := html.Parse(strings.NewReader(body))
			if w.Code != http.StatusOK || err != nil {
				t.Fatalf("%s: status %d, %v", path, w.Code, err)
			}
			var stream *html.Node
			for n := range doc.Descendants() {
				if attr(n, "id") == "organization-stream" {
					stream = n
				}
			}
			rendered, err := strconv.ParseInt(attr(stream, "data-rendered-at"), 10, 64)
			if attr(stream, "data-member") != login.member || attr(stream, "data-session") != login.session ||
				attr(stream, "data-session-expires") != strconv.FormatInt(login.expires.UnixMilli(), 10) ||
				err != nil || rendered < before || rendered > after {
				t.Fatalf("%s for %s: stream %v, rendered between %d and %d", path, login.session, stream.Attr, before, after)
			}
			for _, secret := range []string{token, hex.EncodeToString(hash[:]), base64.RawURLEncoding.EncodeToString(hash[:]), base64.StdEncoding.EncodeToString(hash[:])} {
				if strings.Contains(body, secret) {
					t.Errorf("%s for %s carries the token or its hash", path, login.session)
				}
			}
			for _, other := range logins {
				if other.session != login.session && strings.Contains(body, other.session) || other.member != login.member && strings.Contains(body, other.member) {
					t.Errorf("%s for %s carries %s's session or member", path, login.session, other.session)
				}
			}
		}
	}
}
