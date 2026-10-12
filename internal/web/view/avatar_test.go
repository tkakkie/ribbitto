package view

import (
	"fmt"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestAvatarInitial(t *testing.T) {
	for _, tt := range []struct{ name, displayName, handle, want string }{
		{"English", "Alice", "alice", "A"},
		{"Japanese", "田中", "tanaka", "田"},
		{"handle fallback", "", "alice", "a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := avatarInitial(tt.displayName, tt.handle); got != tt.want {
				t.Errorf("initial = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAvatarColour(t *testing.T) {
	for i := byte(0); i < 6; i++ {
		want := fmt.Sprintf("bg-pastel-%d text-on-pastel-%d", i+1, i+1)
		for _, id := range []kernel.ID{{i}, {15: i}} {
			if got := avatarColour(id); got != want || avatarColour(id) != got {
				t.Errorf("colour for %x = %q, want stable %q", id, got, want)
			}
		}
	}
}
