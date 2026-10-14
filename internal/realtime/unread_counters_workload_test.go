package realtime_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
	"github.com/tkakkie/ribbitto/internal/unread"
)

// Fixed before evidence: see unread-counters-workload.md for the rationale.
const (
	counterStreams       = 5000
	counterReadRate      = 100
	counterMovePeriod    = 10 * time.Second
	counterJoinPeriod    = 5 * time.Second
	counterDetectPeriod  = 5 * time.Second
	counterRepairPeriod  = 30 * time.Second
	counterSmallMembers  = 100
	counterMediumMembers = 1000
	counterLargeMembers  = 5000
	counterReadStates    = 4 // Equal unread, read, half-read, half-read/missing quarters.
	counterMovedMessages = 4
)

type counterSlot struct {
	at                    time.Duration
	kind                  string
	index, member, cohort int
	topic                 bool
	round                 int
}

// Relative slots, including time zero, in [0, window). No arm or database state
// can change the offered load. Stable ties are post, read, move, join, detect, repair.
func counterSchedule(rate int, window time.Duration) ([]counterSlot, error) {
	if (rate != 5 && rate != 20) || window <= 0 {
		return nil, fmt.Errorf("counter schedule needs 5 or 20 posts/s and a positive window")
	}
	var slots []counterSlot
	for _, load := range []struct {
		kind   string
		period time.Duration
	}{
		{"post", time.Second / time.Duration(rate)}, {"read", time.Second / counterReadRate},
		{"move", counterMovePeriod}, {"join", counterJoinPeriod}, {"detect", counterDetectPeriod}, {"repair", counterRepairPeriod},
	} {
		for i, at := 0, time.Duration(0); at < window; i, at = i+1, at+load.period {
			s := counterSlot{at: at, kind: load.kind, index: i}
			if s.kind == "read" {
				s.member, s.round = i%counterStreams, i/counterStreams
				s.topic = i%2 == 1
			}
			s.cohort = i % 3
			slots = append(slots, s)
		}
	}
	slices.SortStableFunc(slots, func(a, b counterSlot) int { return cmp.Compare(a.at, b.at) })
	return slots, nil
}

type counterCohort struct {
	channel, source, destination kernel.ID
	messages                     []kernel.ID
	sequences                    []int64
	members                      int
}

type counterWorkload struct {
	pool           *pgxpool.Pool
	counters       bool
	members        []org.Membership
	channel, topic kernel.ID
	first          int64
	cohorts        []counterCohort
	signup         *org.SignUp
	now            func() time.Time
}

// Fixture-only SQL. Seed enough interleaved history for every advancing read in the
// whole window; live posts cannot determine whether the offered reads have work.
func counterWorkloadFixture(t *testing.T, h *counterMatrix, window time.Duration) *counterWorkload {
	t.Helper()
	if len(h.members) != counterStreams {
		t.Fatal("counter workload requires 5000 distinct members")
	}
	w := &counterWorkload{members: h.members, channel: h.channel, now: time.Now}
	w.topic = conversationtest.Topic(t, h.pool, h.members[0].Organization.ID, h.channel, "Read history").ID
	reads := int((window-1)/(time.Second/counterReadRate)) + 1
	p := costReadPlan{capacity: 2 * ((reads + counterStreams - 1) / counterStreams)}
	var err error
	w.first, err = p.seed(t.Context(), h.pool, h.members[0], h.channel, h.base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), `UPDATE message SET topic_id=$3 WHERE organization_id=$1 AND channel_id=$2 AND (event_seq-$4)%2=1`, h.members[0].Organization.ID, h.channel, w.topic, w.first); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{counterSmallMembers, counterMediumMembers, counterLargeMembers} {
		c := conversationtest.Channel(t, h.pool, h.members[0].Organization.ID, fmt.Sprintf("move-%d", n), false)
		d := conversationtest.Topic(t, h.pool, c.OrganizationID, c.ID, "Destination")
		seed := costReadPlan{capacity: counterMovedMessages}
		first, err := seed.seed(t.Context(), h.pool, h.members[0], c.ID, c.DefaultTopicID)
		if err != nil {
			t.Fatal(err)
		}
		cohort := counterCohort{channel: c.ID, source: c.DefaultTopicID, destination: d.ID, members: n}
		rows := counterQuery(t, h.pool, `SELECT id,event_seq FROM message WHERE organization_id=$1 AND channel_id=$2 ORDER BY event_seq`, c.OrganizationID, c.ID)
		for rows.Next() {
			var id kernel.ID
			var seq int64
			if err := rows.Scan(&id, &seq); err != nil {
				t.Fatal(err)
			}
			cohort.messages = append(cohort.messages, id)
			cohort.sequences = append(cohort.sequences, seq)
		}
		rows.Close()
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		ids := make([]kernel.ID, n)
		for i, m := range h.members[:n] {
			ids[i] = m.Member.ID
		}
		if _, err := h.pool.Exec(t.Context(), `WITH members AS (SELECT id,(ordinality-1)%$5 AS state FROM unnest($3::uuid[]) WITH ORDINALITY AS m(id,ordinality)), locks AS (
 INSERT INTO channel_read SELECT $1,$2,id FROM members RETURNING member_id
 ), ranges AS (
 INSERT INTO read_range SELECT $1,$2,id,lo,hi FROM members JOIN locks ON member_id=id
 CROSS JOIN (VALUES ($4::bigint,$4::bigint+1),($4::bigint+2,$4::bigint+3)) r(lo,hi) WHERE state IN (2,3)
 UNION ALL SELECT $1,$2,id,0,$4::bigint+4 FROM members JOIN locks ON member_id=id WHERE state=1
 ) INSERT INTO topic_read_floor SELECT $1,$6,id,$2,$4::bigint+4 FROM members`, c.OrganizationID, c.ID, ids, first, counterReadStates, d.ID); err != nil {
			t.Fatal(err)
		}
		w.cohorts = append(w.cohorts, cohort)
	}
	counterSQLTables(t, h.pool)
	// Identical persisted initial counters for both arms, with one missing quarter.
	// These are fixture rows, never accounting or bytea-carrier evidence.
	scopes := append([]counterCohort{{channel: w.channel, source: h.base, destination: w.topic, members: counterStreams}}, w.cohorts...)
	for i, c := range scopes {
		full, half := 0, 0
		if i > 0 {
			full, half = counterMovedMessages, counterMovedMessages/2
		}
		ids := make([]kernel.ID, c.members)
		for i, m := range h.members[:c.members] {
			ids[i] = m.Member.ID
		}
		if _, err := h.pool.Exec(t.Context(), `WITH members AS (SELECT id,(ordinality-1)%$6 AS state FROM unnest($3::uuid[]) WITH ORDINALITY AS m(id,ordinality)), bundles AS (
 INSERT INTO counter_bundle SELECT $1,$2,id,CASE state WHEN 1 THEN $7::bigint WHEN 2 THEN $8::bigint ELSE 0 END,true,0 FROM members WHERE state<>3 RETURNING member_id,n
 ) INSERT INTO counter_topic_read SELECT $1,$2,member_id,topic,CASE WHEN topic=$4 THEN n ELSE 0 END FROM bundles CROSS JOIN unnest(ARRAY[$4::uuid,$5::uuid]) topic`, h.members[0].Organization.ID, c.channel, ids, c.source, c.destination, counterReadStates, full, half); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *counterWorkload) bind(pool *pgxpool.Pool, counters bool, hasher *identity.Hasher) {
	w.pool, w.counters = pool, counters
	w.signup = orgpg.NewSignUp(pool, hasher, true, func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) }, func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) })
}

// Dispatch only: admission, persisted-read results, completions and drain belong
// to #957c4b. Each call has a fresh write capture and the existing single account.
func (w *counterWorkload) dispatch(ctx context.Context, s counterSlot) (*counterAccount, error) {
	m, channel := w.members[s.member], w.channel
	if s.kind == "move" {
		channel = w.cohorts[s.cohort].channel
	}
	writeCtx, write := counterSQLContext(ctx, m, channel)
	if s.kind != "detect" && s.kind != "repair" {
		ctx = writeCtx // Standalone scans own their SQL, without write-maintenance hooks.
	}
	counterSQLUseIntervals(write)
	write.join = s.kind == "join"
	var c counterCohort
	if s.kind == "move" {
		c = w.cohorts[s.cohort]
		if s.index/3%2 == 1 {
			c.source, c.destination = c.destination, c.source
		}
		write.moveFrom, write.moves = c.source, c.sequences
	}
	ctx, account := counterAccountContext(ctx, w.now)
	var err error
	switch s.kind {
	case "post":
		posting := conversationpg.NewPagePosting(w.pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.PostReadWriter { return counterSQLPostReads{tx} }, nil)
		_, err = posting.PostToTopic(ctx, m, channel, nil, "workload post")
	case "read":
		cursor := w.first + int64(2*s.round)
		err = platform.InTxDetachedCommit(ctx, w.pool, 5*time.Second, func(tx platform.Tx) error {
			scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}
			if s.topic {
				_, e := counterTopicWriter(nil).Read(ctx, tx, unread.TopicScope{Scope: scope, TopicID: w.topic}, m.Member.JoinedEventSeq, cursor+1)
				return e
			}
			_, e := counterFeedWriter().Read(ctx, tx, scope, m.Member.JoinedEventSeq, cursor)
			return e
		})
	case "move":
		var notice int64
		brancher := conversationpg.NewBrancher(w.pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.ReadRangeWriter { return counterBranchReads{tx, &notice} }, nil)
		_, err = brancher.Branch(ctx, m, channel, conversation.Branch{From: c.source, To: &c.destination, Messages: c.messages}, func(conversation.Topic) string { return "workload notice" })
	case "join":
		handle := fmt.Sprintf("loadjoin%d", s.index)
		_, err = w.signup.SignUp(ctx, handle, handle, handle+"@example.org", "long enough password")
	case "detect":
		if w.counters {
			_, err = counterDetect(ctx, w.pool, write)
		}
	case "repair":
		if w.counters {
			err = counterRepair(ctx, w.pool, write)
		}
	default:
		err = fmt.Errorf("unknown counter workload operation %q", s.kind)
	}
	if err == nil {
		err = write.err
	}
	account.finish(err, false)
	return account, err
}
