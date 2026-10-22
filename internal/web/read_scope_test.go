package web

import (
	"net/url"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestReadScope(t *testing.T) {
	m := org.Membership{
		Organization: org.Organization{ID: kernel.ID{1}},
		Member:       org.Member{ID: kernel.ID{3}, OrganizationID: kernel.ID{1}},
	}
	channel := kernel.ID{2}
	want := unread.Scope{OrganizationID: kernel.ID{1}, ChannelID: channel, MemberID: kernel.ID{3}}
	for _, tt := range []struct {
		name string
		form url.Values
	}{
		{"no organisation field", url.Values{"cursor": {"4"}}},
		{"empty field", url.Values{"organization": {""}}},
		{"malformed field", url.Values{"organization": {"acme"}}},
		{"upper-case field", url.Values{"organization": {"01000000-0000-0000-0000-00000000000A"}}},
		{"field naming the member's organisation", url.Values{"organization": {"01000000-0000-0000-0000-000000000000"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := readScope(tt.form, m, channel); got != want {
				t.Fatalf("readScope = %+v, want %+v", got, want)
			}
		})
	}
}
