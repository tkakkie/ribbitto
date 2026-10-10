package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// TestFirstUnreadBench compares the actual bound statement and two moved-first
// forms. It uses a disposable migrated database, default planner settings and
// the second EXPLAIN (warm buffers). Run outside CI with
// RIBBITTO_FIRST_UNREAD_BENCH=1 go test -run '^TestFirstUnreadBench$' -v ./cmd/ribbitto.
func TestFirstUnreadBench(t *testing.T) {
	if os.Getenv("RIBBITTO_FIRST_UNREAD_BENCH") != "1" || os.Getenv("CI") != "" {
		t.Skip("set RIBBITTO_FIRST_UNREAD_BENCH=1 outside CI")
	}
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "first-unread", "general")
	ids := []kernel.ID{f.Channel.DefaultTopicID}
	for i := range 50 {
		ids = append(ids, conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, fmt.Sprint(i)).ID)
	}
	var version string
	feedRequire(t, pool.QueryRow(t.Context(), "SELECT version()").Scan(&version))
	t.Log(version)
	settings, err := pool.Query(t.Context(), `SELECT name,setting FROM pg_settings
 WHERE category LIKE 'Query Tuning%' AND source <> 'default' ORDER BY name`)
	feedRequire(t, err)
	defer settings.Close()
	for settings.Next() {
		values, err := settings.Values()
		feedRequire(t, err)
		t.Fatalf("non-default planner setting: %v", values)
	}
	feedRequire(t, settings.Err())
	rows, err := pool.Query(t.Context(), "SELECT name,setting,unit FROM pg_settings WHERE name IN ('shared_buffers','work_mem','effective_cache_size','jit','max_parallel_workers_per_gather','random_page_cost') ORDER BY name")
	feedRequire(t, err)
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		feedRequire(t, err)
		t.Logf("setting: %v", values)
	}
	feedRequire(t, rows.Err())
	capture := &firstUnreadCapture{}
	config := pool.Config()
	config.ConnConfig.Tracer = capture
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	feedRequire(t, err)
	defer traced.Close()
	for _, tc := range []struct {
		name    string
		history int64
	}{
		{"history-10k", 10000}, {"history-100k", 100000}, {"history-1M", 1000000},
		{"late-moves", 100000}, {"no-moves", 100000}, {"all-read", 100000},
		{"fragmented", 100000}, {"high-floor", 100000},
		{"all-unread", 100000}, {"early-unread", 100000}, {"dense-moves", 100000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := func(sql string, args ...any) { _, err := pool.Exec(t.Context(), sql, args...); feedRequire(t, err) }
			exec("TRUNCATE message")
			start, floor := tc.history/10+1, tc.history*2
			if tc.name == "late-moves" {
				start = tc.history*9/10 + 1
			}
			moveSize := int64(1000)
			if tc.name == "early-unread" || tc.name == "dense-moves" {
				start = 2
			}
			if tc.name == "dense-moves" {
				moveSize = tc.history
			}
			if tc.name == "high-floor" {
				floor = 1000000000000
			}
			exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq,moved_event_seq)
 SELECT $1,$2,$3,$4,'history',s,CASE WHEN $8 AND s BETWEEN $6 AND $6+$9 THEN $7::bigint+2000+s END
 FROM generate_series(2,$5::bigint+1) s`, f.OrganizationID, f.Channel.ID, ids[0], f.MemberID, tc.history, start, floor, tc.name != "no-moves", moveSize-1)
			for i, id := range ids[1:] {
				exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,$3,$4,'other',s FROM generate_series($5::bigint,$5::bigint+1999) s`, f.OrganizationID, f.Channel.ID, id, f.MemberID, tc.history*3+int64(i)*2000)
			}
			exec("ANALYZE message")
			read := [][2]int64{{start + 1, start + 1000}}
			switch tc.name {
			case "no-moves", "all-unread", "early-unread", "dense-moves":
				read = nil
			case "all-read":
				read = [][2]int64{{start, start + 1000}}
			case "fragmented":
				read = nil
				for i := int64(0); i < 10000; i++ {
					read = append(read, [2]int64{2 + i*2, 3 + i*2})
				}
			}
			floors := make([]int64, len(ids))
			for i := range floors {
				floors[i] = floor
				if i > 0 {
					floors[i] = tc.history*5 + 100000
				}
			}
			for _, n := range []int{1, 51} {
				if n == 51 && tc.name != "history-100k" {
					continue
				}
				feedRequire(t, platform.InSnapshot(t.Context(), traced, func(s platform.Snapshot) error {
					_, _, err := conversationpg.TopicUnreadIn(s).Count(t.Context(), f.OrganizationID, f.Channel.ID, 2, ids[:n], floors[:n], read, &ids[0])
					return err
				}))
				sql, args := capture.sql, capture.args
				variants := firstUnreadVariants(t, sql)
				var want string
				for _, candidate := range []string{"current", "sorted", "materialized"} {
					query := variants[candidate]
					rows, err := pool.Query(t.Context(), query, args...)
					feedRequire(t, err)
					values, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
						ID           kernel.ID
						Count, First int64
					}])
					feedRequire(t, err)
					result := fmt.Sprint(values)
					if candidate == "current" {
						want = result
					} else if result != want {
						t.Fatalf("%s result=%s want=%s", candidate, result, want)
					}
					var raw []byte
					for range 2 {
						feedRequire(t, pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&raw))
					}
					var plans []struct {
						Plan map[string]any
						Time float64 `json:"Execution Time"`
					}
					feedRequire(t, json.Unmarshal(raw, &plans))
					p := plans[0]
					t.Logf("topics=%d %s result=%s total_hits=%v reads=%v execution_ms=%.3f", n, candidate, result, p.Plan["Shared Hit Blocks"], p.Plan["Shared Read Blocks"], p.Time)
					firstUnreadLogScans(t, candidate, p.Plan)
				}
			}
		})
	}
}

type firstUnreadCapture struct {
	sql  string
	args []any
}

func (c *firstUnreadCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "AS first_unread") {
		c.sql, c.args = d.SQL, d.Args
	}
	return ctx
}
func (*firstUnreadCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func firstUnreadVariants(t *testing.T, sql string) map[string]string {
	t.Helper()
	// Normalize the adopted CTE back to #746, preserving generated parameters.
	if begin := strings.Index(sql, ", moved AS MATERIALIZED (\n"); begin != -1 {
		bodyStart := begin + len(", moved AS MATERIALIZED (\n")
		end := bodyStart + strings.Index(sql[bodyStart:], "\n)\nSELECT t.topic_id")
		body := strings.Replace(sql[bodyStart:end], "FROM topics t CROSS JOIN message m", "FROM message m", 1)
		body = body[strings.Index(body, "SELECT m.event_seq"):]
		sql = sql[:begin] + sql[end+2:]
		from := strings.Index(sql, "(SELECT event_seq FROM moved WHERE")
		to := from + strings.Index(sql[from:], "ORDER BY event_seq LIMIT 1)") + len("ORDER BY event_seq LIMIT 1)")
		sql = sql[:from] + "(" + body + " ORDER BY m.event_seq LIMIT 1)" + sql[to:]
	}
	start := strings.LastIndex(sql, "(SELECT m.event_seq FROM message m")
	if start < 0 {
		t.Fatal("moved-first branch not found")
	}
	end := strings.Index(sql[start:], "ORDER BY m.event_seq LIMIT 1)") + start
	if end < start {
		t.Fatal("moved-first order not found")
	}
	body := sql[start+1 : end]
	if !strings.Contains(body, "m.moved_event_seq > t.floor") {
		t.Fatal("expected moved-first branch")
	}
	tail := sql[end+len("ORDER BY m.event_seq LIMIT 1)"):]
	selectedStart := strings.Index(body, "t.topic_id = ")
	selectedEnd := selectedStart + strings.Index(body[selectedStart:], "\n")
	selection := strings.TrimSpace(body[selectedStart:selectedEnd])
	materialized := sql[:start] + "(SELECT event_seq FROM moved WHERE " + selection + " ORDER BY event_seq LIMIT 1)" + tail
	materialized = strings.Replace(materialized, "\nSELECT t.topic_id::uuid", ", moved AS MATERIALIZED (\n"+strings.Replace(body, "FROM message m", "FROM topics t CROSS JOIN message m", 1)+"\n)\nSELECT t.topic_id::uuid", 1)
	return map[string]string{
		"current":      sql,
		"sorted":       sql[:start] + "(SELECT event_seq FROM (" + body + "ORDER BY m.moved_event_seq) moved ORDER BY event_seq LIMIT 1)" + tail,
		"materialized": materialized,
	}
}

func firstUnreadLogScans(t *testing.T, candidate string, p map[string]any) {
	t.Helper()
	if p["Relation Name"] == "message" {
		t.Logf("%s message node=%v index=%v loops=%v returned=%v filtered=%v recheck=%v hits=%v reads=%v node_ms=%v", candidate, p["Node Type"], p["Index Name"], p["Actual Loops"], p["Actual Rows"], p["Rows Removed by Filter"], p["Rows Removed by Index Recheck"], p["Shared Hit Blocks"], p["Shared Read Blocks"], p["Actual Total Time"])
	}
	for _, child := range childrenOfPlan(p) {
		firstUnreadLogScans(t, candidate, child)
	}
}
func childrenOfPlan(p map[string]any) []map[string]any {
	var out []map[string]any
	if children, ok := p["Plans"].([]any); ok {
		for _, child := range children {
			out = append(out, child.(map[string]any))
		}
	}
	return out
}
