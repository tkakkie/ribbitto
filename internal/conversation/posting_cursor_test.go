package conversation_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func TestSendCursor(t *testing.T) {
	m := org.Membership{
		Organization: org.Organization{ID: kernel.ID{1}},
		Member:       org.Member{ID: kernel.ID{2}, OrganizationID: kernel.ID{1}},
	}
	wantSent := conversation.Sent{
		Message: conversation.Message{
			ID:             kernel.ID{5},
			OrganizationID: kernel.ID{1},
			ChannelID:      kernel.ID{3},
			TopicID:        kernel.ID{6},
			MemberID:       kernel.ID{2},
			Body:           "hi",
			EventSeq:       7,
		},
		Topic: kernel.ID{6},
	}
	successCalls := []string{"begin", "sequence", "default", "insert", "previous", "read", "append", "commit", "raise"}
	txRejectCalls := []string{"begin", "sequence"}

	for _, tt := range []struct {
		name      string
		cursor    *int64
		wantErr   bool
		wantCalls []string
	}{
		{name: "nil", cursor: nil, wantCalls: successCalls},
		{name: "0", cursor: new(int64(0)), wantCalls: successCalls},
		{name: "6", cursor: new(int64(6)), wantCalls: successCalls},
		{name: "7", cursor: new(int64(7)), wantErr: true, wantCalls: txRejectCalls},
		{name: "8", cursor: new(int64(8)), wantErr: true, wantCalls: txRejectCalls},
		{name: "MaxInt64-2", cursor: new(int64(math.MaxInt64 - 2)), wantErr: true, wantCalls: txRejectCalls},
		{name: "-1", cursor: new(int64(-1)), wantErr: true, wantCalls: nil},
		{name: "MinInt64", cursor: new(int64(math.MinInt64)), wantErr: true, wantCalls: nil},
		{name: "MaxInt64-1", cursor: new(int64(math.MaxInt64 - 1)), wantErr: true, wantCalls: nil},
		{name: "MaxInt64", cursor: new(int64(math.MaxInt64)), wantErr: true, wantCalls: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &postingFake{t: t, topic: kernel.ID{6}}
			p := conversation.NewPagePosting(f,
				func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
				func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
				func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f },
				func(tx platform.Tx) conversation.PostReadWriter { f.bound(tx); return f }, nil, nil, nil, f)
			got, err := p.Send(t.Context(), m, kernel.ID{3}, conversation.Send{Body: "hi", Cursor: tt.cursor})
			if gotErr := errors.Is(err, conversation.ErrInvalidPostCursor); gotErr != tt.wantErr {
				t.Errorf("cursor %s: errors.Is(%v, ErrInvalidPostCursor) = %t, want %t", tt.name, err, gotErr, tt.wantErr)
			} else if !tt.wantErr && err != nil {
				t.Errorf("cursor %s: err = %v, want nil", tt.name, err)
			}
			want := wantSent
			if tt.wantErr {
				want = conversation.Sent{}
			}
			if got != want {
				t.Errorf("cursor %s: got = %+v, want %+v", tt.name, got, want)
			}
			if !reflect.DeepEqual(f.calls, tt.wantCalls) {
				t.Errorf("cursor %s: calls = %v, want %v", tt.name, f.calls, tt.wantCalls)
			}
		})
	}
}
