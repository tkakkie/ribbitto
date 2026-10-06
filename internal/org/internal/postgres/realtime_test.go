package postgres_test

import (
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommittedSequences(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := orgtest.Organization(t, pool, "watermark", "watermark", 7)
	other := orgtest.Organization(t, pool, "watermark-other", "watermark-other", 3)
	reader := postgres.NewSequences(pool)
	unknown := kernel.ID{0xee}
	got, err := reader.CommittedSequences(ctx, []kernel.ID{f, other, unknown})
	requireNoError(t, err)
	if want := map[kernel.ID]int64{f: 7, other: 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommittedSequences = %v, want %v (an unknown organisation is left out)", got, want)
	}
	got, err = reader.CommittedSequences(ctx, []kernel.ID{f})
	requireNoError(t, err)
	if want := map[kernel.ID]int64{f: 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CommittedSequences(f) = %v, want %v (an existing organisation not asked for is left out)", got, want)
	}
	got, err = reader.CommittedSequences(ctx, nil)
	requireNoError(t, err)
	if len(got) != 0 {
		t.Fatalf("CommittedSequences(nil) = %v, want empty", got)
	}
}
