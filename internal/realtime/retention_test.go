package realtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type retentionDB struct {
	sqlcgen.DBTX
	afterQuery func()
}

func (d *retentionDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := d.DBTX.Query(ctx, sql, args...)
	if err == nil && d.afterQuery != nil {
		f := d.afterQuery
		d.afterQuery = nil
		f() // The SELECT has its snapshot; cleanup commits before rows are decoded.
	}
	return rows, err
}

type retentionSender struct{ send func(realtime.Outgoing) error }

func (s *retentionSender) Send(_ context.Context, out realtime.Outgoing) error { return s.send(out) }

func (s *retentionSender) Heartbeat(context.Context) error { return nil }

// The external tests cannot use stream_test.go's in-package helpers.
type allowAll struct{}

func (allowAll) MayReceive(context.Context, domain.ID, string, realtime.Event) (bool, error) {
	return true, nil
}

type renderMessages struct{}

func (renderMessages) Render(_ context.Context, _ realtime.Subscription, e realtime.Event) (realtime.Outgoing, error) {
	return realtime.Outgoing{ID: e.Seq, Name: "message"}, nil
}

func TestCachedEventsCursorAboveLog(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f := pgtest.OrganizationWithOwner(t, pool, "restored", "general")
	for range 2 {
		_, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "hello")
		must(err)
	}
	cached := realtime.NewCachedEvents(t.Context(), postgres.NewEventReader(pool, postgres.EventKinds()), realtime.NewHub(), 8, time.Minute)
	got, err := cached.EventsAfter(ctx, f.OrganizationID, 2, 1)
	must(err)
	if len(got) != 1 || got[0].Seq != 3 || realtime.CachedLen(cached) != 1 {
		t.Fatalf("cache not primed with event 3: %v", got)
	}
	// Roll the log back to sequence 1 while a full batch survives. A real
	// restore happens with ribbitto stopped, so caches start empty; this only
	// pins that the zero-limit re-check applies the upper bound to a hit.
	tx, err := pool.Begin(ctx)
	must(err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "DELETE FROM message WHERE organization_id = $1 AND event_seq > 1", f.OrganizationID)
	must(err)
	_, err = tx.Exec(ctx, "DELETE FROM event_log WHERE organization_id = $1 AND seq > 1", f.OrganizationID)
	must(err)
	_, err = tx.Exec(ctx, "UPDATE organization SET event_seq = 1 WHERE id = $1", f.OrganizationID)
	must(err)
	must(tx.Commit(ctx))
	got, err = cached.EventsAfter(ctx, f.OrganizationID, 2, 1)
	if !errors.Is(err, realtime.ErrCursorExpired) || len(got) != 0 {
		t.Fatalf("cached batch above restored log: %v, %v; want no events and ErrCursorExpired", got, err)
	}
}

func TestRetentionReplay(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"boundary", "below", "cached", "snapshot", "open", "idle"} {
		t.Run(mode, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			f := pgtest.OrganizationWithOwner(t, pool, "retention", "general")
			post := func() {
				_, err := postgres.NewPostingStore(pool).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "kept message")
				must(err)
			}
			post()
			post()
			_, err := pool.Exec(ctx, "UPDATE event_log SET created_at = '2000-01-01' WHERE organization_id = $1", f.OrganizationID)
			must(err)
			reader := postgres.NewEventReader(pool, postgres.EventKinds())
			expire := func() {
				must(postgres.NewEventCleaner(pool).ExpireEvents(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
			}
			var events realtime.EventReader = reader
			cursor, wantCursor := int64(1), int64(1)
			want := []string{"reset"}
			switch mode {
			case "cached":
				cached := realtime.NewCachedEvents(t.Context(), reader, realtime.NewHub(), 8, time.Minute)
				_, err := cached.EventsAfter(ctx, f.OrganizationID, 1, 1)
				must(err)
				events = cached
				expire()
			case "snapshot":
				events = postgres.NewEventReader(&retentionDB{DBTX: pool, afterQuery: expire}, postgres.EventKinds())
				want, wantCursor = []string{"message", "reset"}, 2
			case "open":
				want, wantCursor = []string{"message", "reset"}, 2
			case "boundary", "idle":
				expire()
				cursor, wantCursor, want = 3, 4, []string{"message"}
				if mode == "boundary" {
					post()
				} else {
					// Commit only after the idle SELECT took its empty snapshot.
					events = postgres.NewEventReader(&retentionDB{DBTX: pool, afterQuery: post}, postgres.EventKinds())
				}
			default:
				cursor, wantCursor = 2, 2
				expire()
			}
			hub := realtime.NewHub()
			hub.Raise(f.OrganizationID, 4) // Also wakes the idle read after its snapshot.
			sent := 0
			sender := retentionSender{send: func(out realtime.Outgoing) error {
				if sent >= len(want) || out.Name != want[sent] {
					t.Fatalf("send %d: %+v, want %v", sent, out, want)
				}
				sent++
				if mode == "open" && sent == 1 {
					expire() // An already-open stream must check its next batch too.
				}
				if sent == len(want) && out.Name == "message" {
					cancel()
				}
				return nil
			}}
			s := realtime.Stream{Hub: hub, Events: events, BatchSize: 1, Authorizer: allowAll{}, Renderer: renderMessages{}}
			got, err := s.Run(ctx, realtime.Subscription{Organization: f.OrganizationID, Channel: f.Channel.ID}, cursor, &sender)
			if got != wantCursor || sent != len(want) || (err != nil && !errors.Is(err, context.Canceled)) {
				t.Fatalf("Run = %d, %v; sent %d; want cursor %d, %v", got, err, sent, wantCursor, want)
			}
			var messages, joined, sequence int64
			must(pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE organization_id = $1 AND event_seq > 1", f.OrganizationID).Scan(&messages))
			must(pool.QueryRow(t.Context(), "SELECT joined_event_seq FROM member WHERE organization_id = $1 AND id = $2", f.OrganizationID, f.MemberID).Scan(&joined))
			must(pool.QueryRow(t.Context(), "SELECT event_seq FROM organization WHERE id = $1", f.OrganizationID).Scan(&sequence))
			if messages != sequence-1 || joined != 1 {
				t.Fatalf("retention changed unread inputs: messages=%d, sequence=%d, joined=%d", messages, sequence, joined)
			}
		})
	}
}
