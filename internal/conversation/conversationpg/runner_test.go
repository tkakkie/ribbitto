package conversationpg

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// The runner commits on nil, and a failed commit, here event_log's deferred
// gap check, comes back through it as is.
func TestTxRunnerCommits(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	organizationID := orgtest.Organization(t, pool, "acme", "acme", 0)
	accountID := identitytest.Account(t, pool, "alice@example.org", "alice")
	memberID := orgtest.Member(t, pool, organizationID, accountID, org.RoleMember, "alice", 1)
	channel := conversationtest.Channel(t, pool, organizationID, "general", false)
	runner := newTxRunner(pool)
	requireNoError(t, runner.InTx(ctx, func(tx platform.Tx) error {
		_, err := writerIn(tx).InsertMessage(ctx, organizationID, channel.ID, channel.DefaultTopicID, memberID, "hello", 1)
		return err
	}))
	var n int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM message").Scan(&n))
	if n != 1 {
		t.Fatalf("%d messages after commit, want 1", n)
	}
	err := runner.InTx(ctx, func(tx platform.Tx) error {
		// The update succeeds; only the deferred trigger at commit refuses it.
		seq, err := orgpg.SequenceIn(tx).NextEventSeq(ctx, organizationID)
		if err != nil || seq != 1 {
			t.Fatalf("NextEventSeq in the callback = %d, %v; want 1", seq, err)
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || err != error(pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, "without event_log rows") {
		t.Fatalf("commit without an event = %v, want the gap check's error as is", err)
	}
	var seq int64
	requireNoError(t, pool.QueryRow(ctx, "SELECT event_seq FROM organization WHERE id = $1", organizationID).Scan(&seq))
	if seq != 0 {
		t.Fatalf("event_seq = %d after the failed commit, want 0", seq)
	}
}

func TestRunnerCallbackErrors(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	failure := errors.New("callback failed")
	if err := newTxRunner(pool).InTx(t.Context(), func(platform.Tx) error { return failure }); err != failure {
		t.Fatalf("InTx = %v, want callback's error as is", err)
	}
	if err := newSnapshotRunner(pool).InSnapshot(t.Context(), func(platform.Snapshot) error { return failure }); err != failure {
		t.Fatalf("InSnapshot = %v, want callback's error as is", err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
