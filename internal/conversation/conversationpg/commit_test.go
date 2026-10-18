package conversationpg_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// commitTrace cancels at COMMIT's entry, after the use case's callback has
// succeeded. The transaction's writes must still use the request context.
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
	}{
		{name: "confirmed commit raises once"},
		{name: "unknown commit does not raise", unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := pgtest.New(t)
			acme := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
			if tc.unknown {
				// Like the platform runner tests, stall COMMIT in a deferred
				// trigger until the detached limit expires without a reply.
				_, err := pool.Exec(t.Context(), `CREATE FUNCTION stall_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_sleep(30); RETURN NULL; END $$;
CREATE CONSTRAINT TRIGGER stall_commit AFTER INSERT ON message
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stall_commit();`)
				requireNoError(t, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var pid uint32
			commits := 0
			config := pool.Config()
			config.ConnConfig.Tracer = commitTrace(func(conn *pgx.Conn) {
				pid = conn.PgConn().PID()
				commits++
				cancel()
			})
			traced, err := pgxpool.NewWithConfig(t.Context(), config)
			requireNoError(t, err)
			t.Cleanup(traced.Close)
			// End the stalled transaction before pgtest drops its database;
			// the server may still be running after pgx closes its connection.
			t.Cleanup(func() {
				if tc.unknown && pid != 0 {
					_, err := pool.Exec(context.Background(), "SELECT pg_terminate_backend($1)", pid)
					requireNoError(t, err)
				}
			})
			notifier := &recordingNotifier{t: t, pool: pool}
			posting := conversationpg.NewPosting(traced, eventSequence, appendEvents, notifier)
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
			var messages int
			requireNoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM message WHERE organization_id = $1", acme.OrganizationID).Scan(&messages))
			if err != nil || messages != 1 || posted.EventSeq != 2 {
				t.Errorf("post after cancellation: error=%v, messages=%d, sequence=%d; want success, 1, 2", err, messages, posted.EventSeq)
			}
			if !slices.Equal(notifier.raised, []raise{{acme.OrganizationID, 2, 2}}) {
				t.Errorf("raised %+v, want exactly one raise of confirmed sequence 2", notifier.raised)
			}
		})
	}
}
