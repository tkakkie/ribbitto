package unread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type postState struct {
	readingState
	merge func(Scope, int64, conversation.SequenceRange) error
}

func (p postState) Merge(_ context.Context, s Scope, j int64, r conversation.SequenceRange) error {
	return p.merge(s, j, r)
}

func TestPostReads(t *testing.T) {
	failure := errors.New("failed step")
	scope := Scope{OrganizationID: kernel.ID{1}, ChannelID: kernel.ID{2}, MemberID: kernel.ID{3}}
	// Decision 5: posting has no page-scope or floor input; every call is one union.
	for _, failure := range []error{nil, failure} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			calls := 0
			state := postState{merge: func(s Scope, j int64, r conversation.SequenceRange) error {
				calls++
				if s != scope || j != 5 || r != (conversation.SequenceRange{Lo: 9, Hi: 12}) {
					t.Fatal("incorrect merge arguments")
				}
				return failure
			}}
			reads := NewPostReads(func(platform.Tx) RangeWriter { return state })
			err := reads.Read(t.Context(), platform.Tx{}, scope, 5, conversation.SequenceRange{Lo: 9, Hi: 12})
			if !errors.Is(err, failure) || failure != nil && !strings.Contains(err.Error(), "reading own post:") {
				t.Fatalf("error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("merge calls = %d, want 1", calls)
			}
		})
	}
}
