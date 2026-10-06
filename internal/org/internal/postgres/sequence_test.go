package postgres_test

import (
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestEventCursorIn(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	id := orgtest.Organization(t, pool, "acme", "Acme", 7)
	requireNoError(t, platform.InSnapshot(ctx, pool, func(snapshot platform.Snapshot) error {
		cursor := postgres.EventCursorIn(snapshot)
		if got, err := cursor.EventSeq(ctx, id); err != nil || got != 7 {
			t.Fatalf("EventSeq = %d, %v; want 7, nil", got, err)
		}
		if got, err := cursor.EventSeq(ctx, kernel.ID{0xee}); got != 0 || !errors.Is(err, org.ErrNotFound) {
			t.Fatalf("unknown organization: EventSeq = %d, %v; want 0, ErrNotFound", got, err)
		}
		return nil
	}))
}
