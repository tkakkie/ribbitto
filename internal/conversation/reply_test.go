package conversation

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestSameReply(t *testing.T) {
	a, b := kernel.ID{1}, kernel.ID{2}
	for _, tc := range []struct {
		name      string
		stored    *Reply
		requested *kernel.ID
		want      bool
	}{
		{"absent", nil, nil, true},
		{"added", nil, &a, false},
		{"removed", &Reply{MessageID: a}, nil, false},
		{"changed", &Reply{MessageID: a}, &b, false},
		{"unchanged", &Reply{MessageID: a}, &a, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sameReply(tc.stored, tc.requested) != tc.want {
				t.Fatal("reply identity comparison disagrees")
			}
		})
	}
}
