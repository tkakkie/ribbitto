package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func feedRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkedRead(t *testing.T, want bool) func(bool, error) error {
	t.Helper()
	return func(changed bool, err error) error {
		t.Helper()
		if (err == nil && changed != want) || (err != nil && changed) {
			return fmt.Errorf("changed=%t, want %t (error=%v)", changed, want, err)
		}
		return err
	}
}

func feedFixture(t *testing.T) (*pgxpool.Pool, unread.Scope) {
	t.Helper()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "feed", "general")
	posting := conversationpg.NewPosting(pool, compose.PostingSequence, compose.PostingEvents, nil)
	member := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
	for range 5 {
		_, err := posting.Post(t.Context(), member, f.Channel.ID, "message")
		feedRequire(t, err)
	}
	return pool, unread.Scope{OrganizationID: f.OrganizationID, ChannelID: f.Channel.ID, MemberID: f.MemberID}
}

func feedRanges(t *testing.T, pool *pgxpool.Pool, scope unread.Scope, want []conversation.SequenceRange) {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT lo,hi FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 ORDER BY lo", scope.OrganizationID, scope.ChannelID, scope.MemberID)
	feedRequire(t, err)
	defer rows.Close()
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversation.SequenceRange, error) {
		var r conversation.SequenceRange
		err := row.Scan(&r.Lo, &r.Hi)
		return r, err
	})
	feedRequire(t, err)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges=%v, want %v", got, want)
	}
}

func topicFloor(t *testing.T, pool *pgxpool.Pool, s unread.TopicScope, want int64) {
	t.Helper()
	var got int64
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT floor_seq FROM topic_read_floor WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3 AND topic_id=$4", s.OrganizationID, s.ChannelID, s.MemberID, s.TopicID).Scan(&got))
	if got != want {
		t.Fatalf("floor=%d, want %d", got, want)
	}
}

type pageTrace struct {
	*platform.QueryCounter
	names map[string]int
	hook  func(string)
}

func (p *pageTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	p.QueryCounter.TraceQueryStart(ctx, conn, data)
	if fields := strings.Fields(data.SQL); len(fields) > 2 && fields[1] == "name:" {
		p.names[fields[2]]++
		p.hook(fields[2])
	}
	return ctx
}
