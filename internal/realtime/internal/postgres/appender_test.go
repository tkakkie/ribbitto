package postgres_test

import (
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres"
)

// realtime's appender keeps the audience: NULL stays organisation-wide, a
// member of the organisation survives the write and reads back, and another
// organisation's member is rejected. Today's publishers always append NULL,
// so without this an appender that dropped the audience would widen delivery.
func TestAppenderAudience(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "audience", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "audience-other", "general")
	appendOne := func(audience *kernel.ID) (int64, error) {
		var seq int64
		err := platform.InTx(ctx, pool, func(tx platform.Tx) error {
			if err := pgxbridge.Tx(tx).QueryRow(ctx, "UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq", f.OrganizationID).Scan(&seq); err != nil {
				return err
			}
			return postgres.AppenderIn(tx).Append(ctx, f.OrganizationID, seq, "test.audience", audience, []byte(`{}`))
		})
		return seq, err
	}
	reader := postgres.NewReader(pool, orgpg.BoundsIn, eventKinds())
	for _, audience := range []*kernel.ID{nil, &f.MemberID} {
		seq, err := appendOne(audience)
		requireNoError(t, err)
		got, err := reader.EventsAfter(ctx, f.OrganizationID, seq-1, 1)
		requireNoError(t, err)
		if len(got) != 1 || !reflect.DeepEqual(got[0].AudienceMemberID, audience) {
			t.Fatalf("audience %v read back as %+v", audience, got)
		}
	}
	if _, err := appendOne(&other.MemberID); err == nil {
		t.Fatal("appended an event whose audience is another organisation's member")
	}
}
