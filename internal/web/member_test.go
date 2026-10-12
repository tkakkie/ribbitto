package web

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestMemberName(t *testing.T) {
	for _, tt := range []struct {
		name, displayName string
		want              string
		fallback          bool
	}{
		{"plain", "Alice", `<bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span>`, false},
		{"right-to-left name is isolated", "مريم", `<bdi class="font-semibold text-fg">مريم</bdi> <span class="text-muted">@alice</span>`, false},
		{"escaped", "<b>Al</b>", `<bdi class="font-semibold text-fg">&lt;b&gt;Al&lt;/b&gt;</bdi>`, false},
		// A stored name from before the blank rule falls back to the handle.
		{"blank-looking", "ㅤ", `<bdi class="font-semibold text-fg">@alice</bdi>`, true},
		{"combining marks only", "́́", `<bdi class="font-semibold text-fg">@alice</bdi>`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := view.MemberName(memberDisplayName(tt.displayName), "alice").Render(t.Context(), &b); err != nil {
				t.Fatal(err)
			}
			got := b.String()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("got %s, want it to contain %s", got, tt.want)
			}
			if tt.fallback {
				if strings.Count(got, "@alice") != 1 || strings.Contains(got, tt.displayName) {
					t.Fatalf("fallback shows the blank name or repeats the handle: %s", got)
				}
			}
		})
	}
}

func TestMessageAuthorAvatar(t *testing.T) {
	for _, tt := range []struct{ name, displayName, want string }{
		{"display name", "田中", "田"},
		{"empty name", "", "a"},
		{"legacy blank name", "ㅤ", "a"},
		{"multi-code-point initial", "A\u0301lice", "A\u0301"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := conversation.Entry{Message: conversation.Message{
				ID: kernel.ID{9}, MemberID: kernel.ID{4}, Body: "message text first",
			}, DisplayName: tt.displayName, Handle: "alice"}
			model := viewMessage("acme", entry)
			if model.MemberID != entry.MemberID {
				t.Fatal("author's member ID was lost in the view mapping")
			}
			for _, component := range []templ.Component{view.MessageItem(model), view.LiveMessageItem(model)} {
				var b strings.Builder
				if err := component.Render(t.Context(), &b); err != nil {
					t.Fatal(err)
				}
				got := b.String()
				avatar := strings.Index(got, `<span aria-hidden="true" class="flex size-9`)
				if avatar < 0 || !strings.Contains(got[avatar:], "bg-pastel-5 text-on-pastel-5") || !strings.Contains(got[avatar:], "<bdi>"+tt.want+"</bdi>") {
					t.Fatalf("missing fixed-size, hidden avatar with the author's initial and colour: %s", got)
				}
				if body := strings.Index(got, `>message text first</p>`); body < 0 || body > avatar {
					t.Fatal("message text must precede the avatar in page and live markup")
				}
			}
		})
	}
}
