package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
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

type retentionSender struct {
	recorder
	send func(Outgoing) error
}

func (s *retentionSender) Send(_ context.Context, out Outgoing) error { return s.send(out) }

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
			reader := postgres.NewEventReader(pool)
			expire := func() {
				must(postgres.NewEventCleaner(pool).ExpireEvents(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
			}
			var events EventReader = reader
			cursor, wantCursor := int64(1), int64(1)
			want := []string{"reset"}
			switch mode {
			case "cached":
				cached := NewCachedEvents(reader, NewHub(), 8, time.Minute)
				_, err := cached.EventsAfter(ctx, f.OrganizationID, 1, 1)
				must(err)
				events = cached
				expire()
			case "snapshot":
				events = postgres.NewEventReader(&retentionDB{DBTX: pool, afterQuery: expire})
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
					events = postgres.NewEventReader(&retentionDB{DBTX: pool, afterQuery: post})
				}
			default:
				cursor, wantCursor = 2, 2
				expire()
			}
			hub := NewHub()
			hub.Raise(f.OrganizationID, 4) // Also wakes the idle read after its snapshot.
			sent := 0
			sender := retentionSender{send: func(out Outgoing) error {
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
			s := Stream{Hub: hub, Events: events, BatchSize: 1, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}
			got, err := s.Run(ctx, Subscription{Organization: f.OrganizationID, Channel: f.Channel.ID}, cursor, &sender)
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

// A failed cleanup is retried with the next cutoff; cancellation stops the loop.
type retentionCleanerFunc func(context.Context, time.Time) error

func (f retentionCleanerFunc) ExpireEvents(ctx context.Context, cutoff time.Time) error {
	return f(ctx, cutoff)
}

func TestRetentionTicks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks, calls, done := make(chan time.Time), make(chan time.Time), make(chan struct{})
	r := Retention{Period: time.Hour, Events: retentionCleanerFunc(func(ctx context.Context, cutoff time.Time) error {
		calls <- cutoff
		return errors.New("retry")
	})}
	go func() { r.Run(ctx, ticks); close(done) }()
	for _, now := range []time.Time{time.Unix(10000, 0), time.Unix(20000, 0)} {
		ticks <- now
		if got := receive(t, calls); !got.Equal(now.Add(-time.Hour)) {
			t.Fatalf("cutoff = %v", got)
		}
	}
	cancel()
	receive(t, done)
}
