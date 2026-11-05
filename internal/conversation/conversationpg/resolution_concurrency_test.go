package conversationpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func TestResolutionConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		firstBranch, secondBranch bool
		firstEvents, finalEvents  []realtime.EventKind
		secondError               error
	}{
		{
			name: "resolve first then branch", secondBranch: true,
			firstEvents: []realtime.EventKind{conversation.KindResolutionChanged},
			finalEvents: []realtime.EventKind{conversation.KindResolutionChanged, conversation.KindMessagesMoved, conversation.KindPosted, conversation.KindResolutionChanged},
		},
		{
			name: "branch first then resolve", firstBranch: true,
			firstEvents: []realtime.EventKind{conversation.KindMessagesMoved, conversation.KindPosted},
			finalEvents: []realtime.EventKind{conversation.KindMessagesMoved, conversation.KindPosted},
			secondError: conversation.ErrConclusionConflict, // HTTP 409 for the just-moved choice.
		},
		{
			name:        "two concurrent resolves",
			firstEvents: []realtime.EventKind{conversation.KindResolutionChanged},
			finalEvents: []realtime.EventKind{conversation.KindResolutionChanged},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := pgtest.New(t)
			var workers sync.WaitGroup
			defer workers.Wait()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			f := conversationtest.OrganizationWithOwner(t, pool, "concurrency", "general")
			m := membership(f.OrganizationID, f.MemberID)
			m.Member.OrganizationID = f.OrganizationID
			source := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "decision").ID
			destination := f.Channel.DefaultTopicID
			msg, err := newPosting(pool).PostToTopic(ctx, m, f.Channel.ID, &source, "conclusion")
			requireNoError(t, err)
			before := eventSeq(t, pool, f.OrganizationID)
			check := func(conclusion, location kernel.ID, kinds []realtime.EventKind) {
				t.Helper()
				assertResolutionCommit(t, ctx, pool, f.OrganizationID, f.Channel.ID, source, msg.ID, conclusion, location, before, kinds)
			}
			locked := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			results := [2]chan error{make(chan error, 1), make(chan error, 1)}
			start := func(i int, branch bool) {
				sequences := resolutionSequenceBarrier(locked[i], release[i])
				workers.Go(func() {
					if branch {
						_, err := conversationpg.NewBrancher(pool, sequences, compose.PostingEvents, compose.BranchReads, nil).Branch(ctx, m, f.Channel.ID,
							conversation.Branch{Messages: []kernel.ID{msg.ID}, From: source, To: &destination}, func(conversation.Topic) string { return "moved" })
						results[i] <- err
					} else {
						results[i] <- conversationpg.NewResolution(pool, sequences, compose.PostingEvents, nil).Resolve(ctx, m, f.Channel.ID, source, msg.ID)
					}
				})
			}
			awaitLock := func(i int) {
				t.Helper()
				select {
				case <-locked[i]:
				case err := <-results[i]:
					t.Fatalf("operation %d finished before its lock barrier: %v", i, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			start(0, tc.firstBranch)
			awaitLock(0)
			start(1, tc.secondBranch)
			// Each subtest owns its database. Require a real sequence-lock wait
			// before releasing the first transaction, not merely a started goroutine.
			for {
				var blocked bool
				requireNoError(t, pool.QueryRow(ctx, `SELECT EXISTS (
					SELECT FROM pg_stat_activity waiter JOIN pg_stat_activity holder
					ON holder.pid = ANY(pg_blocking_pids(waiter.pid))
					WHERE waiter.datname = current_database() AND holder.datname = waiter.datname
					AND waiter.wait_event_type = 'Lock'
					AND waiter.query LIKE '%UPDATE organization SET event_seq%'
					AND holder.query LIKE '%UPDATE organization SET event_seq%'
				)`).Scan(&blocked))
				if blocked {
					break
				}
				select {
				case err := <-results[1]:
					t.Fatalf("second operation did not wait for the organisation: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			check(kernel.ID{}, source, nil)
			close(release[0])
			awaitLock(1)
			requireNoError(t, <-results[0])
			// The second transaction now owns the organisation lock but cannot
			// read the topic yet, exposing the first committed state for inspection.
			conclusion, location := msg.ID, source
			if tc.firstBranch {
				conclusion, location = kernel.ID{}, destination
			}
			check(conclusion, location, tc.firstEvents)
			close(release[1])
			if err := <-results[1]; !errors.Is(err, tc.secondError) {
				t.Fatalf("second operation = %v, want %v", err, tc.secondError)
			}
			if tc.secondBranch {
				conclusion, location = kernel.ID{}, destination
			}
			check(conclusion, location, tc.finalEvents)
		})
	}
}

type resolutionSequenceFunc func(context.Context, kernel.ID) (int64, error)

func (f resolutionSequenceFunc) NextEventSeq(ctx context.Context, id kernel.ID) (int64, error) {
	return f(ctx, id)
}

// Keep the real sequence allocation and its row lock; pause only its first
// return, since branching takes more sequences within the same transaction.
func resolutionSequenceBarrier(locked chan<- struct{}, release <-chan struct{}) conversation.EventSequenceIn {
	return func(tx platform.Tx) conversation.EventSequence {
		sequences := compose.PostingSequence(tx)
		first := true
		return resolutionSequenceFunc(func(ctx context.Context, id kernel.ID) (int64, error) {
			seq, err := sequences.NextEventSeq(ctx, id)
			if err == nil && first {
				first = false
				close(locked)
				select {
				case <-release:
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
			return seq, err
		})
	}
}

func assertResolutionCommit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, organization, channel, topic, message, wantConclusion, wantLocation kernel.ID, before int64, kinds []realtime.EventKind) {
	t.Helper()
	var conclusion, location kernel.ID
	var seq int64
	var valid bool
	requireNoError(t, pool.QueryRow(ctx, `SELECT
		coalesce(t.conclusion_message_id, '00000000-0000-0000-0000-000000000000'),
		m.topic_id, o.event_seq, NOT EXISTS (
			SELECT FROM topic candidate WHERE candidate.organization_id = o.id
			AND candidate.conclusion_message_id IS NOT NULL AND NOT EXISTS (
				SELECT FROM message conclusion WHERE conclusion.organization_id = candidate.organization_id
				AND conclusion.channel_id = candidate.channel_id AND conclusion.topic_id = candidate.id
				AND conclusion.id = candidate.conclusion_message_id))
		FROM organization o JOIN topic t ON t.organization_id = o.id
		JOIN message m ON m.organization_id = o.id AND m.channel_id = t.channel_id
		WHERE o.id = $1 AND t.channel_id = $2 AND t.id = $3 AND m.id = $4`, organization, channel, topic, message).Scan(&conclusion, &location, &seq, &valid))
	if !valid || conclusion != wantConclusion || location != wantLocation || seq != before+int64(len(kinds)) {
		t.Fatalf("committed state: valid=%v conclusion=%s location=%s seq=%d; want conclusion=%s location=%s seq=%d", valid, realtime.FormatPayloadID(conclusion), realtime.FormatPayloadID(location), seq, realtime.FormatPayloadID(wantConclusion), realtime.FormatPayloadID(wantLocation), before+int64(len(kinds)))
	}
	events, err := realtimepg.NewReader(pool, orgpg.BoundsIn, eventKinds(t)).EventsAfter(ctx, organization, before, 20)
	requireNoError(t, err)
	if len(events) != len(kinds) {
		t.Fatalf("committed events = %d, want %d", len(events), len(kinds))
	}
	for i, event := range events {
		if event.Kind != kinds[i] || event.Seq != before+int64(i)+1 {
			t.Fatalf("event %d = %s at %d, want %s at %d", i, event.Kind, event.Seq, kinds[i], before+int64(i)+1)
		}
		if event.Kind == conversation.KindResolutionChanged {
			var data []byte
			var public bool
			requireNoError(t, pool.QueryRow(ctx, `SELECT data, audience_member_id IS NULL FROM event_log
				WHERE organization_id = $1 AND seq = $2`, organization, event.Seq).Scan(&data, &public))
			var payload map[string]string
			requireNoError(t, json.Unmarshal(data, &payload))
			if len(payload) != 2 || payload["channel_id"] != realtime.FormatPayloadID(channel) || payload["topic_id"] != realtime.FormatPayloadID(topic) || !public {
				t.Fatalf("stored resolution payload = %s, audience NULL = %v; want exactly channel_id=%s and topic_id=%s, audience NULL", data, public, realtime.FormatPayloadID(channel), realtime.FormatPayloadID(topic))
			}
			gotChannel, gotTopic, err := conversation.DecodeResolutionChanged(event.Payload)
			if err != nil || gotChannel != channel || gotTopic != topic || event.ChannelID != channel || event.Topics != nil || event.AudienceMemberID != nil {
				t.Fatalf("resolution event routing: channel=%s topic=%s routed channel=%s topics nil=%v audience nil=%v, error=%v", realtime.FormatPayloadID(gotChannel), realtime.FormatPayloadID(gotTopic), realtime.FormatPayloadID(event.ChannelID), event.Topics == nil, event.AudienceMemberID == nil, err)
			}
		}
	}
}
