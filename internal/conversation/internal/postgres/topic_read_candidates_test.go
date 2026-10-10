package postgres_test

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestTopicReadCandidates(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "candidates", "general")
	a := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "a")
	b := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "b")
	empty := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "empty")
	writer := postgres.NewWriterForTest(pool)
	messages := make(map[int64]kernel.ID)
	for _, seq := range []int64{8, 10, 11, 12, 13, 14, 15, 16, 18, 20, 22, 24} {
		topic := a.ID
		if seq == 11 || seq == 13 || seq == 15 || seq == 14 {
			topic = f.Channel.DefaultTopicID
		}
		m, err := writer.InsertMessage(t.Context(), f.OrganizationID, f.Channel.ID, topic, f.MemberID, "candidate", seq)
		requireNoError(t, err)
		messages[seq] = m.ID
	}
	// Two moves retain the posting sequence and only the latest move cursor.
	for _, move := range []struct {
		from, to kernel.ID
		seq      int64
	}{{f.Channel.DefaultTopicID, b.ID, 17}, {b.ID, a.ID, 21}} {
		_, err := writer.MoveMessages(t.Context(), f.OrganizationID, f.Channel.ID, move.from, move.to, []kernel.ID{messages[14]}, move.seq)
		requireNoError(t, err)
	}
	_, err := pool.Exec(t.Context(), "UPDATE message SET moved_event_seq=23 WHERE organization_id=$1 AND event_seq IN (16,18)", f.OrganizationID)
	requireNoError(t, err)
	for _, tc := range []struct {
		name                  string
		topic                 kernel.ID
		cursor, prefix, floor int64
		read, want            []conversation.SequenceRange
	}{
		{"whole channel", a.ID, 12, 10, 9, nil, []conversation.SequenceRange{{Lo: 9, Hi: 11}, {Lo: 12, Hi: 13}}},
		{"floor and read set", a.ID, 22, 10, 15, []conversation.SequenceRange{{Lo: 20, Hi: 21}}, []conversation.SequenceRange{{Lo: 14, Hi: 15}, {Lo: 21, Hi: 24}}},
		{"moved read", a.ID, 22, 10, 15, []conversation.SequenceRange{{Lo: 14, Hi: 15}, {Lo: 20, Hi: 21}}, []conversation.SequenceRange{{Lo: 21, Hi: 24}}},
		{"latest move after cursor", a.ID, 20, 10, 15, nil, []conversation.SequenceRange{{Lo: 19, Hi: 22}}},
		{"no predecessor", a.ID, 8, 1, 0, nil, []conversation.SequenceRange{{Lo: 1, Hi: 10}}},
		{"newest", a.ID, 24, 24, 23, nil, []conversation.SequenceRange{{Lo: 23, Hi: 25}}},
		{"empty", empty.ID, 24, 1, 0, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoError(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				got, err := conversationpg.TopicReadCandidatesIn(tx).Ranges(t.Context(), f.OrganizationID, f.Channel.ID, tc.topic, tc.cursor, tc.prefix, tc.floor, tc.read)
				requireNoError(t, err)
				if !slices.Equal(got, tc.want) {
					t.Fatalf("ranges=%v, want %v", got, tc.want)
				}
				return nil
			}))
		})
	}
}

func TestTopicReadCandidatesScope(t *testing.T) {
	for _, dimension := range []string{"organization", "channel", "topic"} {
		t.Run(dimension, func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "scope", "general")
			other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
			a := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "a")
			org, channel, topic, member := f.OrganizationID, f.Channel.ID, a.ID, f.MemberID
			// Globally unique IDs otherwise hide missing organisation/channel checks.
			// Relax only the message FKs needed for single-scope decoys in this clone.
			_, err := pool.Exec(t.Context(), "ALTER TABLE message DROP CONSTRAINT message_organization_id_channel_id_fkey, DROP CONSTRAINT message_topic_fkey")
			requireNoError(t, err)
			switch dimension {
			case "organization":
				org, member = other.OrganizationID, other.MemberID
			case "channel":
				channel = other.Channel.ID
			case "topic":
				topic = f.Channel.DefaultTopicID
			}
			writer := postgres.NewWriterForTest(pool)
			for _, seq := range []int64{5, 10, 15} {
				id := f.Channel.DefaultTopicID
				if seq == 10 {
					id = a.ID
				}
				_, err := writer.InsertMessage(t.Context(), f.OrganizationID, f.Channel.ID, id, f.MemberID, "in scope", seq)
				requireNoError(t, err)
			}
			for _, seq := range []int64{9, 11} {
				_, err := writer.InsertMessage(t.Context(), org, channel, topic, member, "single-scope decoy", seq)
				requireNoError(t, err)
			}
			want := []conversation.SequenceRange{{Lo: 6, Hi: 15}}
			if dimension == "topic" {
				want = []conversation.SequenceRange{{Lo: 10, Hi: 11}}
			}
			requireNoError(t, platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
				got, err := conversationpg.TopicReadCandidatesIn(tx).Ranges(t.Context(), f.OrganizationID, f.Channel.ID, a.ID, 30, 1, 0, nil)
				requireNoError(t, err)
				if !slices.Equal(got, want) {
					t.Fatalf("ranges=%v, want %v", got, want)
				}
				return nil
			}))
		})
	}
}

func TestTopicReadCandidatesStatementCount(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "statements", "general")
	_, err := pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
	 SELECT $1,$2,$3,$4,'candidate',s FROM generate_series(2,301) s`, f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, f.MemberID)
	requireNoError(t, err)
	counter := platform.NewQueryCounter()
	config := pool.Config()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	defer traced.Close()
	requireNoError(t, platform.InTx(t.Context(), traced, func(tx platform.Tx) error {
		for _, count := range []int64{0, 1, 300} {
			before := counter.Counts()
			got, err := conversationpg.TopicReadCandidatesIn(tx).Ranges(t.Context(), f.OrganizationID, f.Channel.ID, f.Channel.DefaultTopicID, count+1, 2, 1, nil)
			requireNoError(t, err)
			if int64(len(got)) != count || counter.Counts().Queries-before.Queries != 1 {
				t.Fatalf("%d candidates: rows=%d, statements=%d", count, len(got), counter.Counts().Queries-before.Queries)
			}
		}
		return nil
	}))
}
