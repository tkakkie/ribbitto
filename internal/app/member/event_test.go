package member_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestJoinedPayload(t *testing.T) {
	joined := domain.ID{0: 0xef, 15: 1}
	data, err := member.EncodeJoined(joined)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := member.DecodeJoined(data); err != nil || got.MemberID != joined {
		t.Fatalf("round trip = %+v, %v; want %v", got, err, joined)
	}
}
