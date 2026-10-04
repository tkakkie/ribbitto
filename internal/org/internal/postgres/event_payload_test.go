package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// Org's encoder replaced SQL's jsonb_build_object (#398). Comparing with
// that SQL also catches an encoder and decoder that agree on the wrong shape.
func TestEventPayloadCompatibility(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	t.Run("member.joined", func(t *testing.T) {
		id := kernel.ID{0: 0xab, 6: 0x7c, 8: 0x9d, 15: 4}
		encoded := org.EncodeJoined(id)
		var stored []byte
		requireNoError(t, pool.QueryRow(t.Context(), "SELECT jsonb_build_object('member_id', $1::uuid)",
			pgtype.UUID{Bytes: id, Valid: true}).Scan(&stored))
		got, err := org.DecodeJoined(stored)
		if want := (org.Joined{MemberID: id}); err != nil || got != want {
			t.Fatalf("decoding SQL-built %s = %+v, %v; want %+v", stored, got, err, want)
		}
		var equal bool
		requireNoError(t, pool.QueryRow(t.Context(), "SELECT $1::jsonb = $2::jsonb", encoded, stored).Scan(&equal))
		if !equal {
			t.Fatalf("encoder wrote %s; SQL built %s", encoded, stored)
		}
	})
}
