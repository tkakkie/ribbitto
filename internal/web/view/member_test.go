package view_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/view"
)

func TestMemberName(t *testing.T) {
	for _, tt := range []struct {
		name, displayName string
		want              string
	}{
		{"plain", "Alice", `<bdi class="font-semibold text-fg">Alice</bdi> <span class="text-muted">@alice</span>`},
		{"right-to-left name is isolated", "مريم", `<bdi class="font-semibold text-fg">مريم</bdi> <span class="text-muted">@alice</span>`},
		{"escaped", "<b>Al</b>", `<bdi class="font-semibold text-fg">&lt;b&gt;Al&lt;/b&gt;</bdi>`},
		// A stored name from before the blank rule falls back to the handle.
		{"blank-looking", "ㅤ", `<bdi class="font-semibold text-fg">@alice</bdi>`},
		{"combining marks only", "́́", `<bdi class="font-semibold text-fg">@alice</bdi>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := view.MemberName(tt.displayName, "alice").Render(t.Context(), &b); err != nil {
				t.Fatal(err)
			}
			got := b.String()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("got %s, want it to contain %s", got, tt.want)
			}
			if strings.Contains(tt.name, "blank") || strings.Contains(tt.name, "marks") {
				if strings.Count(got, "@alice") != 1 || strings.Contains(got, tt.displayName) {
					t.Fatalf("fallback shows the blank name or repeats the handle: %s", got)
				}
			}
		})
	}
}
