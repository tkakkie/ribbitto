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
	// Upper-case hex is read as before; only the layout is canonical.
	if got, err := member.DecodeJoined([]byte(`{"member_id":"00000000-0000-0000-0000-00000000000A"}`)); err != nil || got.MemberID != (domain.ID{15: 10}) {
		t.Fatalf("upper-case member ID = %+v, %v", got, err)
	}
}
