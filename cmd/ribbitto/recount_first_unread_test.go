package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

type recountQuery struct {
	sql  string
	args []any
}

type recountTrace struct {
	*platform.QueryCounter
	queries []recountQuery
}

func (c *recountTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.QueryCounter.TraceQueryStart(ctx, conn, d)
	if strings.Contains(d.SQL, "AS first_unread") {
		c.queries = append(c.queries, recountQuery{d.SQL, d.Args})
	}
	return ctx
}

// The replay checks SQL output too: discarding first unread in Go would still
// pay for the selected topic's uncapped moved-candidate materialisation.
func TestSidebarSkipsFirstUnread(t *testing.T) {
	for _, unlisted := range []bool{false, true} {
		t.Run(fmt.Sprintf("unlisted=%t", unlisted), func(t *testing.T) {
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "recount", "general")
			selected := f.Channel.DefaultTopicID
			if unlisted {
				for i := range 50 {
					selected = conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprintf("topic%02d", i)).ID
				}
			}
			m, err := orgpg.NewAuthorizer(pool).Member(t.Context(), &identity.Account{ID: f.AccountID}, "recount")
			feedRequire(t, err)
			// 10k history, 1k unread moves below the floor, one above it.
			_, err = pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq)
 SELECT $1,$2,$3,$4,'history',s,CASE WHEN s < 1002 THEN 30000+s END FROM generate_series(2,10001) s`,
				f.OrganizationID, f.Channel.ID, selected, f.MemberID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), `INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 VALUES ($1,$2,$3,$4,'above floor',20001)`, f.OrganizationID, f.Channel.ID, selected, f.MemberID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), `INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3)`,
				f.OrganizationID, f.Channel.ID, f.MemberID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), `INSERT INTO topic_read_floor (organization_id,channel_id,topic_id,member_id,floor_seq)
 VALUES ($1,$2,$3,$4,20000)`, f.OrganizationID, f.Channel.ID, selected, f.MemberID)
			feedRequire(t, err)
			_, err = pool.Exec(t.Context(), "ANALYZE message")
			feedRequire(t, err)
			if os.Getenv("RIBBITTO_RECOUNT_MEASURE") == "1" {
				var version, settings string
				feedRequire(t, pool.QueryRow(t.Context(), "SELECT version(), current_setting('work_mem') || '/' || current_setting('shared_buffers') || '/' || current_setting('random_page_cost')").Scan(&version, &settings))
				t.Logf("%s; work_mem/shared_buffers/random_page_cost=%s", version, settings)
			}
			trace := &recountTrace{QueryCounter: platform.NewQueryCounter()}
			config := pool.Config()
			config.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(t.Context(), config)
			feedRequire(t, err)
			defer traced.Close()
			reader := conversationpg.NewReader(traced, lookupMembers, lookupAccounts, eventCursor, pageCountsIn)
			sidebar, err := reader.Sidebar(t.Context(), m, f.Channel.ID, &selected)
			feedRequire(t, err)
			if q := trace.Counts(); q.Queries != 8 || q.Begins != 1 || q.Commits != 1 || q.Rollbacks != 0 {
				t.Fatalf("recount statements/transaction = %+v, want 8/1/1/0", q)
			}
			queries := trace.queries
			if len(queries) != 2 {
				t.Fatalf("count statements = %d, want 2", len(queries))
			}
			t.Logf("recount: %+v; topics counted=%d", trace.Counts(), len(sidebar.TopicCounts))
			for _, query := range queries {
				rows, err := pool.Query(t.Context(), query.sql, query.args...)
				feedRequire(t, err)
				values, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
					ID           [16]byte
					Count, First int64
				}])
				feedRequire(t, err)
				repeats := 1
				if os.Getenv("RIBBITTO_RECOUNT_MEASURE") == "1" {
					repeats = 2 // Report the second plan with warm buffers.
				}
				for repeat := range repeats {
					rows, err = pool.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+query.sql, query.args...)
					feedRequire(t, err)
					lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
					feedRequire(t, err)
					for i, line := range lines {
						if strings.TrimSpace(line) == "CTE moved" && i+1 < len(lines) && !strings.Contains(lines[i+1], "never executed") {
							t.Errorf("recount materialized moved candidates: %s", lines[i+1])
						}
						if repeats == 2 && repeat == 1 {
							t.Log(line)
						}
					}
				}
				for _, value := range values {
					if value.First != 0 {
						t.Errorf("recount SQL computed first unread = %d, want 0", value.First)
					}
				}
			}
			page, err := reader.Page(t.Context(), m, f.Channel.ID, &selected, nil)
			feedRequire(t, err)

			if sidebar.Topic == nil || sidebar.Topic.ID != selected || sidebar.TopicCounts[selected] != 100 ||
				!reflect.DeepEqual(sidebar.ChannelCounts, page.ChannelCounts) || !reflect.DeepEqual(sidebar.TopicCounts, page.TopicCounts) {
				t.Fatal("recount lost selection or differs from page counts")
			}
			if page.FeedFirstUnread != 2 || page.TopicFirstUnread != 2 {
				t.Fatalf("page first unread = %d/%d, want 2/2", page.FeedFirstUnread, page.TopicFirstUnread)
			}
			for _, topic := range sidebar.Topics {
				if unlisted && topic.ID == selected {
					t.Fatal("selected topic unexpectedly listed")
				}
			}
		})
	}
}
