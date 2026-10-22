package compose_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type readOracleMessage struct {
	id, topic  kernel.ID
	seq, moved int64
}

type readOracleFloor struct {
	topic kernel.ID
	seq   int64
}

type readOracleState struct {
	messages []readOracleMessage
	ranges   []conversation.SequenceRange
	floors   []readOracleFloor
}

func readOracleLoad(t *testing.T, pool *pgxpool.Pool, scope unread.Scope) readOracleState {
	t.Helper()
	var state readOracleState
	rows, err := pool.Query(t.Context(), `SELECT id,topic_id,event_seq,coalesce(moved_event_seq,0)
		FROM message WHERE organization_id=$1 AND channel_id=$2 ORDER BY event_seq`, scope.OrganizationID, scope.ChannelID)
	feedRequire(t, err)
	state.messages, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (readOracleMessage, error) {
		var message readOracleMessage
		err := row.Scan(&message.id, &message.topic, &message.seq, &message.moved)
		return message, err
	})
	feedRequire(t, err)
	rows, err = pool.Query(t.Context(), `SELECT lo,hi FROM read_range
		WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY lo`, scope.OrganizationID, scope.ChannelID, scope.MemberID)
	feedRequire(t, err)
	state.ranges, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversation.SequenceRange, error) {
		var r conversation.SequenceRange
		err := row.Scan(&r.Lo, &r.Hi)
		return r, err
	})
	feedRequire(t, err)
	rows, err = pool.Query(t.Context(), `SELECT topic_id,floor_seq FROM topic_read_floor
		WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY topic_id`, scope.OrganizationID, scope.ChannelID, scope.MemberID)
	feedRequire(t, err)
	state.floors, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (readOracleFloor, error) {
		var floor readOracleFloor
		err := row.Scan(&floor.topic, &floor.seq)
		return floor, err
	})
	feedRequire(t, err)
	return state
}

// Only the greatest lo <= seq decides membership. Floors and production's
// derived prefixes, gap lists, caps and multiranges never enter this computation.
func readOracleRead(ranges []conversation.SequenceRange, joined, seq int64) bool {
	if len(ranges) == 0 {
		return seq <= joined
	}
	read := false
	for _, r := range ranges {
		if r.Lo > seq {
			break
		}
		read = r.Hi > seq
	}
	return read
}

func readOracleUnread(state readOracleState, joined int64, topic *kernel.ID) (int64, int64) {
	var count, first int64
	for _, message := range state.messages {
		if topic != nil && message.topic != *topic {
			continue
		}
		if !readOracleRead(state.ranges, joined, message.seq) {
			count++
			if first == 0 {
				first = message.seq
			}
		}
	}
	return count, first
}

func readOracleCheckState(state readOracleState, joined int64, model map[int64]bool) []string {
	var violations []string
	if len(state.ranges) > 0 && state.ranges[0].Lo != 0 {
		violations = append(violations, "first range does not start at 0")
	}
	for i, r := range state.ranges {
		if i == 0 {
			continue
		}
		previous := state.ranges[i-1]
		if previous.Hi >= r.Lo {
			violations = append(violations, "ranges overlap or touch")
			continue
		}
		holdsUnread := false
		for _, message := range state.messages {
			if previous.Hi <= message.seq && message.seq < r.Lo && !readOracleRead(state.ranges, joined, message.seq) {
				holdsUnread = true
			}
		}
		if !holdsUnread {
			violations = append(violations, "bounded gap has no unread channel message")
		}
	}
	for _, message := range state.messages {
		read := readOracleRead(state.ranges, joined, message.seq)
		if read != model[message.seq] {
			violations = append(violations, fmt.Sprintf("read set differs from model at %d: read=%t", message.seq, read))
		}
		for _, floor := range state.floors {
			_ = floor
		}
	}
	return violations
}

func readOracleCheckCounts(counts conversation.PageCounts, states map[kernel.ID]readOracleState, channel kernel.ID, topics []kernel.ID, selected *kernel.ID, joined int64) []string {
	var violations []string
	for id, state := range states {
		count, first := readOracleUnread(state, joined, nil)
		if counts.ChannelCounts[id] != min(count, 100) {
			violations = append(violations, "channel count differs from direct computation")
		}
		if id == channel && counts.FeedFirstUnread != first {
			violations = append(violations, "feed first unread differs from direct computation")
		}
	}
	for _, topic := range topics {
		count, first := readOracleUnread(states[channel], joined, &topic)
		if counts.TopicCounts[topic] != min(count, 100) {
			violations = append(violations, "topic count differs from direct computation")
		}
		if selected != nil && topic == *selected && counts.TopicFirstUnread != first {
			violations = append(violations, "topic first unread differs from direct computation")
		}
	}
	return violations
}

func readOracleRows(t *testing.T, pool *pgxpool.Pool, organization *kernel.ID, messages bool) []string {
	t.Helper()
	tables := []string{"read_range", "channel_read", "topic_read_floor"}
	if messages {
		tables = append(tables, "message")
	}
	var result []string
	for _, table := range tables {
		rows, err := pool.Query(t.Context(), fmt.Sprintf("SELECT to_jsonb(r)::text FROM %s r WHERE ($1::uuid IS NULL OR organization_id=$1) ORDER BY to_jsonb(r)::text", table), organization)
		feedRequire(t, err)
		values, err := pgx.CollectRows(rows, pgx.RowTo[string])
		feedRequire(t, err)
		result = append(result, table)
		result = append(result, values...)
	}
	return result
}

func readOracleCheckRows(before, after []string, name string) []string {
	if !slices.Equal(before, after) {
		return []string{name + " changed rows"}
	}
	return nil
}

func TestReadOracleCheckers(t *testing.T) {
	for _, tc := range []struct {
		name, ranges, violation string
		floor, modelRead        bool
	}{
		{name: "clean"},
		{name: "first range", ranges: "(2,3)", violation: "first range does not start at 0"},
		{name: "touching", ranges: "(0,3),(3,4)", violation: "ranges overlap or touch"},
		{name: "overlapping", ranges: "(0,4),(3,5)", violation: "ranges overlap or touch"},
		{name: "empty gap", ranges: "(0,7),(8,9)", violation: "bounded gap has no unread channel message"},
		{name: "floor", floor: true, violation: "topic floor covers an unread message"},
		{name: "model", ranges: "(0,3)", violation: "read set differs from model at 2: read=true"},
		{name: "model read database unread", modelRead: true, violation: "read set differs from model at 2: read=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, scope := feedFixture(t)
			var topic kernel.ID
			feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&topic))
			if tc.ranges != "" || tc.floor {
				_, err := pool.Exec(t.Context(), `INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)`, scope.OrganizationID, scope.ChannelID, scope.MemberID)
				feedRequire(t, err)
			}
			if tc.ranges != "" {
				_, err := pool.Exec(t.Context(), fmt.Sprintf(`INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi)
					SELECT $1,$2,$3,lo,hi FROM (VALUES %s) AS planted(lo,hi)`, tc.ranges), scope.OrganizationID, scope.ChannelID, scope.MemberID)
				feedRequire(t, err)
			}
			if tc.floor {
				_, err := pool.Exec(t.Context(), "INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq) VALUES ($1,$2,$3,$4,6)", scope.OrganizationID, scope.ChannelID, scope.MemberID, topic)
				feedRequire(t, err)
			}
			model := make(map[int64]bool)
			if tc.modelRead {
				model[2] = true
			}
			state := readOracleLoad(t, pool, scope)
			violations := readOracleCheckState(state, 1, model)
			if tc.violation == "" {
				if len(violations) != 0 {
					t.Fatalf("clean state: %v", violations)
				}
			} else if !slices.Contains(violations, tc.violation) {
				t.Fatalf("missing %q: %v", tc.violation, violations)
			}
		})
	}
	t.Run("row preservation", func(t *testing.T) {
		pool, scope := feedFixture(t)
		before := readOracleRows(t, pool, nil, false)
		if violations := readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "clean"); len(violations) != 0 {
			t.Fatal(violations)
		}
		_, err := pool.Exec(t.Context(), "INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)", scope.OrganizationID, scope.ChannelID, scope.MemberID)
		feedRequire(t, err)
		if violations := readOracleCheckRows(before, readOracleRows(t, pool, nil, false), "refused step"); !slices.Contains(violations, "refused step changed rows") {
			t.Fatalf("missing preservation violation: %v", violations)
		}
	})

	// Isolate each comparison: a broken count must not hide a missing first
	// unread check, nor may a topic comparison stand in for a channel comparison.
	for _, field := range []string{"clean counts", "channel count", "topic count", "feed first unread", "topic first unread"} {
		t.Run(field, func(t *testing.T) {
			pool, scope := feedFixture(t)
			var topic kernel.ID
			feedRequire(t, pool.QueryRow(t.Context(), "SELECT default_topic_id FROM channel WHERE id=$1", scope.ChannelID).Scan(&topic))
			member := org.Membership{Organization: org.Organization{ID: scope.OrganizationID}, Member: org.Member{ID: scope.MemberID, JoinedEventSeq: 1}}
			page, err := conversationpg.NewReader(pool, compose.LookupMembers, compose.LookupAccounts, compose.EventCursor, compose.PageCountsIn).Page(t.Context(), member, scope.ChannelID, &topic, nil)
			feedRequire(t, err)
			switch field {
			case "channel count":
				page.ChannelCounts[scope.ChannelID]++
			case "topic count":
				page.TopicCounts[topic]++
			case "feed first unread":
				page.FeedFirstUnread++
			case "topic first unread":
				page.TopicFirstUnread++
			}
			states := map[kernel.ID]readOracleState{scope.ChannelID: readOracleLoad(t, pool, scope)}
			violations := readOracleCheckCounts(page.PageCounts, states, scope.ChannelID, []kernel.ID{topic}, &topic, 1)
			if field == "clean counts" {
				if len(violations) != 0 {
					t.Fatalf("clean counts: %v", violations)
				}
			} else if !slices.Contains(violations, field+" differs from direct computation") {
				t.Fatalf("missing %q comparison: %v", field, violations)
			}
		})
	}
}
