package org_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestJoinedPayload(t *testing.T) {
	joined := domain.ID{0: 0xef, 15: 1}
	data := org.EncodeJoined(joined)
	if got, err := org.DecodeJoined(data); err != nil || got.MemberID != joined {
		t.Fatalf("round trip = %+v, %v; want %v", got, err, joined)
	}
	if channel, topics, err := org.RouteJoined(data); err != nil || channel != (domain.ID{}) || topics != nil {
		t.Fatalf("RouteJoined = %v, %v, %v; want no channel or topics", channel, topics, err)
	}
	// Upper-case hex is read as before; only the layout is canonical.
	if got, err := org.DecodeJoined([]byte(`{"member_id":"00000000-0000-0000-0000-00000000000A"}`)); err != nil || got.MemberID != (domain.ID{15: 10}) {
		t.Fatalf("upper-case member ID = %+v, %v", got, err)
	}
	for _, data := range []string{
		`{"member_id":"invalid"}`,
		`{"member_id":11111111111111111111111111111111111111}`,
		`{"member_id":"00000000x0000x0000x0000x000000000001"}`,
		`{}`,
		`null`,
		`[]`,
	} {
		if got, err := org.DecodeJoined([]byte(data)); err == nil {
			t.Errorf("DecodeJoined(%s) = %+v; want an error", data, got)
		}
	}
}
