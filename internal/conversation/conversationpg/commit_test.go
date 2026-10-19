package conversationpg_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// commitTrace hooks COMMIT after the use case's callback has succeeded.
type commitTrace func(*pgx.Conn)

func (f commitTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == "commit" {
		f(conn)
	}
	return ctx
}

func (commitTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostingCommitCancellation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		unknown bool
		stall   string
	}{
		{name: "confirmed commit raises once", stall: "0.5"},
		{name: "unknown commit does not raise", unknown: true, stall: "30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := pgtest.New(t)
			acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			check, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			// Cancel while the server is finishing COMMIT: the short stall
			// permits confirmation; the long one outlasts the detached limit.
			// pgx also sends a server cancel during timeout cleanup. Only the
			// confirmed probe tolerates it, so the old runner still stores a row.
			_, err := pool.Exec(check, fmt.Sprintf(`CREATE FUNCTION stall_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_sleep(%s);
  RETURN NULL;
EXCEPTION WHEN query_canceled THEN
  IF %t THEN RAISE; END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER stall_commit AFTER INSERT ON message
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stall_commit();`, tc.stall, tc.unknown))
			requireNoError(t, err)
			ctx, cancel := context.WithCancel(check)
			defer cancel()
			var pid uint32
			var timer *time.Timer
			defer func() {
				if timer != nil {
					timer.Stop()
				}
			}()
			commits := 0
			config := pool.Config()
			config.ConnConfig.Tracer = commitTrace(func(conn *pgx.Conn) {
				pid = conn.PgConn().PID()
				commits++
				if tc.unknown {
					cancel()
				} else {
					// Entry cancellation would stop the old runner sending COMMIT.
					timer = time.AfterFunc(100*time.Millisecond, cancel)
				}
			})
			traced, err := pgxpool.NewWithConfig(check, config)
			requireNoError(t, err)
			t.Cleanup(traced.Close)
			// End the stalled transaction before pgtest drops its database;
			// the server may still be running after pgx closes its connection.
			t.Cleanup(func() {
				if tc.unknown && pid != 0 {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, err := pool.Exec(cleanup, "SELECT pg_terminate_backend($1)", pid)
					requireNoError(t, err)
				}
			})
			notifier := &recordingNotifier{t: t, pool: pool}
			posting := conversationpg.NewPosting(traced, compose.PostingSequence, compose.PostingEvents, notifier)
			posted, err := posting.Post(ctx, membership(acme.OrganizationID, acme.MemberID), acme.Channel.ID, "once")
			if ctx.Err() == nil || commits != 1 {
				t.Fatalf("request error=%v, COMMIT calls=%d; want cancellation at one COMMIT", ctx.Err(), commits)
			}
			if tc.unknown {
				if !errors.Is(err, platform.ErrCommitUnknown) {
					t.Errorf("posting error=%v, want ErrCommitUnknown", err)
				}
				if len(notifier.raised) != 0 {
					t.Errorf("raised %+v after an unknown commit, want no raise", notifier.raised)
				}
				return
			}
			// The old runner returns before the server finishes. Wait for its
			// sequence lock to release before checking whether the row exists.
			_, waitErr := pool.Exec(check, "SELECT 1 FROM organization WHERE id = $1 FOR UPDATE", acme.OrganizationID)
			requireNoError(t, waitErr)
			var messages int
			requireNoError(t, pool.QueryRow(check, "SELECT count(*) FROM message WHERE organization_id = $1", acme.OrganizationID).Scan(&messages))
			if err != nil || messages != 1 || posted.EventSeq != 2 {
				t.Errorf("post after cancellation: error=%v, messages=%d, sequence=%d; want success, 1, 2", err, messages, posted.EventSeq)
			}
			if !slices.Equal(notifier.raised, []raise{{acme.OrganizationID, 2, 2}}) {
				t.Errorf("raised %+v, want exactly one raise of confirmed sequence 2", notifier.raised)
			}
		})
	}
}
