package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLockForRetentionAllowsChildInsert(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	id := orgtest.Organization(t, pool, "retention", "Retention", 1)
	account := identitytest.Account(t, pool, "member@example.org", "Member")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	requireNoError(t, platform.InTx(ctx, pool, func(holder platform.Tx) error {
		requireNoError(t, postgres.RetentionBoundaryIn(holder).LockForRetention(ctx, id))
		// Insert without taking a sequence: only the foreign-key check needs
		// the organisation row, and the holder stays open until after commit.
		err := platform.InTx(ctx, pool, func(writer platform.Tx) error {
			_, err := pgxbridge.Tx(writer).Exec(ctx, "SET LOCAL lock_timeout = '200ms'")
			requireNoError(t, err)
			_, err = postgres.RegistrationWriterIn(writer).CreateMember(ctx, id, account, org.RoleMember, 1, "member")
			return err
		})
		if err != nil {
			t.Fatalf("child-row insert under retention lock = %v, want no blocking", err)
		}
		return nil
	}))
}

func TestLockForRetentionBlocksEventSequence(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	id := orgtest.Organization(t, pool, "retention", "Retention", 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	requireNoError(t, platform.InTx(ctx, pool, func(holder platform.Tx) error {
		requireNoError(t, postgres.RetentionBoundaryIn(holder).LockForRetention(ctx, id))
		err := platform.InTx(ctx, pool, func(writer platform.Tx) error {
			_, err := pgxbridge.Tx(writer).Exec(ctx, "SET LOCAL lock_timeout = '200ms'")
			requireNoError(t, err)
			_, err = postgres.SequenceIn(writer).NextEventSeq(ctx, id)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("event_seq increment under retention lock = %v, want lock timeout (55P03)", err)
		}
		return nil
	}))
}

func TestLockForRetentionBlocksRetention(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	id := orgtest.Organization(t, pool, "retention", "Retention", 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	requireNoError(t, platform.InTx(ctx, pool, func(holder platform.Tx) error {
		requireNoError(t, postgres.RetentionBoundaryIn(holder).LockForRetention(ctx, id))
		err := platform.InTx(ctx, pool, func(cleaner platform.Tx) error {
			_, err := pgxbridge.Tx(cleaner).Exec(ctx, "SET LOCAL lock_timeout = '200ms'")
			requireNoError(t, err)
			return postgres.RetentionBoundaryIn(cleaner).LockForRetention(ctx, id)
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("retention lock under retention lock = %v, want lock timeout (55P03)", err)
		}
		return nil
	}))
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
