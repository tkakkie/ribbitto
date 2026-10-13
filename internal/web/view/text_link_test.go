package view

import (
	"bytes"
	"testing"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Extraction must preserve each caller's complete rendered class string.
func TestTextLinkClasses(t *testing.T) {
	page := ChannelPage{Organization: Organization{Slug: "acme"}, Current: Channel{ID: kernel.ID{1}},
		Query: "before=9&unread=4", Members: &MemberPage{Next: &kernel.ID{2}}}
	for _, tc := range []struct {
		name, href, label, classes string
		component                  templ.Component
	}{
		{"home sign-in", "/signin", "hello.sign_in", "inline-flex min-h-11 items-center text-body font-semibold text-brand underline", Hello("", Viewer{SignUpOpen: true})},
		{"home sign-up", "/signup", "signup.title", "inline-flex min-h-11 items-center text-body font-semibold text-brand underline", Hello("", Viewer{SignUpOpen: true})},
		{"sign-in sign-up", "/signup", "signup.title", "inline-flex min-h-11 items-center text-body font-semibold text-brand underline", SignIn("", SignInForm{SignUpOpen: true})},
		{"overview members", panelURL(page, PanelMembers), "people.title", "inline-flex min-h-11 items-center text-brand underline", overviewPanel(page)},
		{"members next", panelURL(page, PanelMembers) + "&after=02000000-0000-0000-0000-000000000000", "people.next", "inline-flex min-h-11 items-center px-5 py-4 text-brand underline", membersPanel(page)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			if err := tc.component.Render(t.Context(), &body); err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(&body)
			if err != nil {
				t.Fatal(err)
			}
			for n := range doc.Descendants() {
				if n.DataAtom != atom.A {
					continue
				}
				attrs := make(map[string]string)
				for _, a := range n.Attr {
					attrs[a.Key] = a.Val
				}
				if attrs["href"] == tc.href {
					if attrs["class"] != tc.classes || n.FirstChild.Data != tc.label {
						t.Fatalf("link classes = %q, label = %q; want %q, %q", attrs["class"], n.FirstChild.Data, tc.classes, tc.label)
					}
					return
				}
			}
			t.Fatalf("no link to %q", tc.href)
		})
	}
}
