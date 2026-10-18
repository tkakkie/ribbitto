package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres/sqlcgen"
)

// mergeBeforeCoverage is the union from c283544^, with identical SQL and locks,
// but without #834's sorting/subtraction. Only valid fixture inputs reach it.
func mergeBeforeCoverage(ctx context.Context, w *Writer, s unread.Scope, joined int64, r conversation.SequenceRange) error {
	p := scopeParams(s)
	if err := w.lock(ctx, p); err != nil {
		return err
	}
	for _, added := range []conversation.SequenceRange{{Lo: 0, Hi: joined + 1}, r} {
		lower := added.Lo
		previous, err := w.queries.RangePredecessor(ctx, sqlcgen.RangePredecessorParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, Lo: added.Lo})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// No predecessor means the same lower bound as the production implementation.
		if err == nil {
			lower = previous.Lo
		}
		removed, err := w.queries.DeleteRangeNeighbours(ctx, sqlcgen.DeleteRangeNeighboursParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, LowerLo: lower, Lo: added.Lo, Hi: added.Hi})
		if err != nil {
			return err
		}
		for _, r := range removed {
			added.Lo = min(added.Lo, r.Lo)
			added.Hi = max(added.Hi, r.Hi)
		}
		if err := w.queries.InsertReadRange(ctx, sqlcgen.InsertReadRangeParams{OrganizationID: p.OrganizationID, ChannelID: p.ChannelID, MemberID: p.MemberID, Lo: added.Lo, Hi: added.Hi}); err != nil {
			return err
		}
	}
	return nil
}

type mergeCostAdapter struct {
	writer   *Writer
	baseline bool
}

func (a mergeCostAdapter) Merge(ctx context.Context, o, c, m kernel.ID, joined, lo, hi int64) error {
	s := unread.Scope{OrganizationID: o, ChannelID: c, MemberID: m}
	r := conversation.SequenceRange{Lo: lo, Hi: hi}
	if a.baseline {
		return mergeBeforeCoverage(ctx, a.writer, s, joined, r)
	}
	return a.writer.Merge(ctx, s, joined, r)
}
func (a mergeCostAdapter) Read(ctx context.Context, m org.Membership, c kernel.ID, _ *kernel.ID, cursor *int64, lo, hi, _ int64) error {
	// Older-history posting's nil cursor takes exactly this production path;
	// feed/topic page reads are measured separately in TestReadAdvanceCost.
	if cursor != nil {
		return fmt.Errorf("measurement requires history posting")
	}
	return a.Merge(ctx, m.Organization.ID, c, m.Member.ID, m.Member.JoinedEventSeq, lo, hi)
}
func mergeCostSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }
func mergeCostEvents(tx platform.Tx) conversation.EventAppender   { return realtimepg.AppenderIn(tx) }

// TestReadMergeCost alternates real committed posting/branching transactions
// against the pre-coverage union on restored, identical fragmented data.
func TestReadMergeCost(t *testing.T) {
	if os.Getenv("RIBBITTO_READ_COST") != "1" {
		t.Skip("set RIBBITTO_READ_COST=1")
	}
	fixture := pgtest.NewEmpty(t)
	pgtest.NewMigrator(t, fixture).Up(t.Context())
	f := conversationtest.OrganizationWithOwner(t, fixture, "merge-cost", "general")
	dest := conversationtest.Topic(t, fixture, f.OrganizationID, f.Channel.ID, "Destination")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID, JoinedEventSeq: 1}}
	seed, err := conversationpg.NewPosting(fixture, mergeCostSequence, mergeCostEvents, nil).Post(t.Context(), m, f.Channel.ID, "seed")
	if err != nil {
		t.Fatal(err)
	}
	q := platform.NewQueryCounter()
	config := fixture.Config()
	config.MaxConns = 1
	config.ConnConfig.Tracer = q
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(t *testing.T, sql string, args ...any) {
		_, err := pool.Exec(t.Context(), sql, args...)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, fragments := range []int{1, 100, 1000} {
		t.Run(fmt.Sprint(fragments), func(t *testing.T) {
			upper := int64(fragments*2 + 4)
			for _, flow := range []string{"merge", "posting", "branching"} {
				var elapsed [2][]time.Duration
				var allocs, statements [2]uint64
				for iteration := range 12 {
					for _, arm := range []int{iteration % 2, 1 - iteration%2} {
						// Restore outside the timers: both arms absorb every fragment, including
						// real posting's own-message and branching's own-notice range.
						exec(t, "DELETE FROM event_log WHERE organization_id=$1 AND seq>$2", f.OrganizationID, seed.EventSeq)
						exec(t, "DELETE FROM message WHERE organization_id=$1 AND id<>$2", f.OrganizationID, seed.ID)
						exec(t, "UPDATE message SET topic_id=$2,moved_event_seq=NULL WHERE id=$1", seed.ID, f.Channel.DefaultTopicID)
						exec(t, "INSERT INTO event_log (organization_id,seq,kind,data) SELECT $1,s,'measurement.gap','{}'::jsonb FROM generate_series($2::bigint+1,$3::bigint) s", f.OrganizationID, seed.EventSeq, upper)
						exec(t, "UPDATE organization SET event_seq=$2 WHERE id=$1", f.OrganizationID, upper)
						exec(t, "DELETE FROM read_range WHERE organization_id=$1", f.OrganizationID)
						exec(t, "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING", f.OrganizationID, f.Channel.ID, f.MemberID)
						exec(t, "INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi) SELECT $1::uuid,$2::uuid,$3::uuid,s,s+1 FROM generate_series(3,$4::bigint,2) s UNION ALL SELECT $1::uuid,$2::uuid,$3::uuid,0,2", f.OrganizationID, f.Channel.ID, f.MemberID, int64(fragments*2+1))
						adapter := func(tx platform.Tx) mergeCostAdapter { return mergeCostAdapter{WriterIn(tx), arm == 0} }
						posting := conversationpg.NewPagePosting(pool, mergeCostSequence, mergeCostEvents, func(tx platform.Tx) conversation.PostReadWriter { return adapter(tx) }, nil, nil)
						branching := conversationpg.NewBrancher(pool, mergeCostSequence, mergeCostEvents, func(tx platform.Tx) conversation.ReadRangeWriter { return adapter(tx) }, nil)
						before := q.Counts()
						var mem0, mem1 runtime.MemStats
						runtime.ReadMemStats(&mem0)
						start := time.Now()
						switch flow {
						case "merge":
							err = platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
								return adapter(tx).Merge(t.Context(), f.OrganizationID, f.Channel.ID, f.MemberID, 1, 3, upper+1)
							})
						case "posting":
							_, err = posting.Post(t.Context(), m, f.Channel.ID, "cost")
						case "branching":
							_, err = branching.Branch(t.Context(), m, f.Channel.ID, conversation.Branch{Messages: []kernel.ID{seed.ID}, From: f.Channel.DefaultTopicID, To: &dest.ID}, func(conversation.Topic) string { return "notice" })
						}
						took := time.Since(start)
						runtime.ReadMemStats(&mem1)
						after := q.Counts()
						if err != nil {
							t.Fatal(err)
						}
						var ranges []conversation.SequenceRange
						rows, err := pool.Query(t.Context(), "SELECT lo,hi FROM read_range WHERE organization_id=$1 ORDER BY lo", f.OrganizationID)
						if err != nil {
							t.Fatal(err)
						}
						for rows.Next() {
							var r conversation.SequenceRange
							if err := rows.Scan(&r.Lo, &r.Hi); err != nil {
								rows.Close()
								t.Fatal(err)
							}
							ranges = append(ranges, r)
						}
						rows.Close()
						if err := rows.Err(); err != nil {
							t.Fatal(err)
						}
						hi := upper + 1
						if flow == "posting" {
							hi = upper + 2
						}
						if flow == "branching" {
							hi = upper + 3
						}
						if !slices.Equal(ranges, []conversation.SequenceRange{{Lo: 0, Hi: 2}, {Lo: 3, Hi: hi}}) {
							t.Fatalf("%s arm=%d persisted union=%v want hi=%d", flow, arm, ranges, hi)
						}
						if iteration >= 2 {
							elapsed[arm] = append(elapsed[arm], took)
							allocs[arm] += mem1.TotalAlloc - mem0.TotalAlloc
							statements[arm] += uint64(after.Queries + after.Begins + after.Commits + after.Rollbacks - before.Queries - before.Begins - before.Commits - before.Rollbacks)
						}
					}
				}
				for arm, samples := range elapsed {
					slices.Sort(samples)
					t.Logf("fragments=%d flow=%s baseline=%t n=%d tx-p50=%s tx-p95=%s bytes/op=%d statements/op=%d", fragments, flow, arm == 0, len(samples), samples[len(samples)/2], samples[len(samples)-1], allocs[arm]/uint64(len(samples)), statements[arm]/uint64(len(samples)))
				}
			}
		})
	}
}
