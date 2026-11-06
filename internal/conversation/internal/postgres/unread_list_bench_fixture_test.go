package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type unreadListBenchGuard struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	started   time.Time
	highWater int64
}

func (g *unreadListBenchGuard) require(err error) {
	g.t.Helper()
	if g.ctx.Err() != nil || time.Since(g.started) >= 2*time.Hour {
		g.t.Fatal("INCONCLUSIVE: stopped at two-hour disposable database session cap; partial results above")
	}
	if err != nil {
		g.t.Fatalf("INCONCLUSIVE: benchmark stopped: %v", err)
	}
}

func (g *unreadListBenchGuard) check() {
	g.t.Helper()
	g.require(nil)
	var size int64
	g.require(g.pool.QueryRow(g.ctx, "SELECT pg_database_size(current_database())").Scan(&size))
	g.highWater = max(g.highWater, size)
	// Stop conservatively with 64 MB reserved for the next bounded insert or
	// temporary query file. No setup statement inserts more than 1,000 rows.
	// pg_database_size covers this clone's tables/indexes, not cluster WAL.
	if size >= 3_000_000_000-64_000_000 {
		g.t.Fatalf("INCONCLUSIVE: stopped before 3 GB database cap: bytes=%d (64 MB reserve); partial results above", size)
	}
}

func (g *unreadListBenchGuard) exec(sql string, args ...any) {
	g.t.Helper()
	g.check()
	_, err := g.pool.Exec(g.ctx, sql, args...)
	g.require(err)
	g.check()
}

func (g *unreadListBenchGuard) id(sql string, args ...any) kernel.ID {
	g.t.Helper()
	g.check()
	var id kernel.ID
	g.require(g.pool.QueryRow(g.ctx, sql, args...).Scan(&id))
	g.check()
	return id
}

func (g *unreadListBenchGuard) lines(sql string) {
	g.t.Helper()
	g.check()
	rows, err := g.pool.Query(g.ctx, sql)
	g.require(err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	g.require(err)
	g.t.Log(strings.Join(lines, "\n"))
	g.check()
}

type unreadListBenchScenario struct {
	name     string
	f        unreadListFixture
	caughtUp org.Membership
	topics   []kernel.ID
}

func (g *unreadListBenchGuard) seed(name string, channels, topics int, largest bool) unreadListBenchScenario {
	g.t.Helper()
	organization := g.id(`INSERT INTO organization (slug,name,event_seq) VALUES ($1,$1,1) RETURNING id`, name)
	member := func(handle string) org.Membership {
		account := g.id(`INSERT INTO account (email,display_name,password_hash) VALUES ($1,'Benchmark','$argon2id$x') RETURNING id`, name+"-"+handle+"@example.org")
		id := g.id(`INSERT INTO member (organization_id,account_id,role,handle,joined_event_seq) VALUES ($1,$2,'member',$3,1) RETURNING id`, organization, account, handle)
		return org.Membership{Organization: org.Organization{ID: organization}, Member: org.Member{ID: id, JoinedEventSeq: 1}}
	}
	scenario := unreadListBenchScenario{name: name, f: unreadListFixture{pool: g.pool, member: member("sparse")}, caughtUp: member("caught-up")}
	writer := member("writer").Member.ID
	for c := range channels {
		channel := g.id(`WITH created AS (
 INSERT INTO channel (organization_id,name,is_default) VALUES ($1,$2,$3) RETURNING *
), default_topic AS (
 INSERT INTO topic (id,organization_id,channel_id,is_default) SELECT default_topic_id,organization_id,id,true FROM created
) SELECT id FROM created`, organization, fmt.Sprintf("channel-%03d", c), c == 0)
		scenario.f.channels = append(scenario.f.channels, channel)
		g.exec(`INSERT INTO topic (organization_id,channel_id,name) SELECT $1,$2,'topic-'||lpad(i::text,3,'0') FROM generate_series(1,$3::int) i`, organization, channel, topics-1)
		g.check()
		rows, err := g.pool.Query(g.ctx, `SELECT id FROM topic WHERE organization_id=$1 AND channel_id=$2 ORDER BY is_default DESC,lower(name),id`, organization, channel)
		g.require(err)
		ids, err := pgx.CollectRows(rows, pgx.RowTo[kernel.ID])
		g.require(err)
		base := int64(c+1) * 1_000_000
		n, cursor := topics, base+int64(topics)*10
		heavy := largest && c == 0
		if heavy {
			// Keep the dense channel newest, so its topics reach the measured
			// first page as well as stressing the sidebar's selected channel.
			base = int64(channels+1) * 1_000_000
			n, cursor = 110_000, base+110_009
		}
		// Heavy channel: 10,000 read singletons, each separated by an unread
		// message, then 90,000 unread: exactly 100,000 unread in this channel.
		for lo := 1; lo <= n; lo += 1000 {
			g.exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq)
 SELECT $1,$2,($3::uuid[])[1+(i-1)%$4],$5,'benchmark',CASE WHEN $6 THEN $7::bigint+i+9 ELSE $7::bigint+i*10 END
 FROM generate_series($8::int,$9::int) i`, organization, channel, ids, topics, writer, heavy, base, lo, min(lo+999, n))
		}
		for _, m := range []org.Membership{scenario.f.member, scenario.caughtUp} {
			g.exec(`INSERT INTO channel_read VALUES ($1,$2,$3)`, organization, channel, m.Member.ID)
			prefix := base + 100
			if heavy {
				prefix = base + 10
			}
			if m.Member.ID == scenario.caughtUp.Member.ID {
				prefix = cursor + 1
			}
			g.exec(`INSERT INTO read_range VALUES ($1,$2,$3,0,$4)`, organization, channel, m.Member.ID, prefix)
		}
		if heavy {
			for lo := 0; lo < 10_000; lo += 1000 {
				g.exec(`INSERT INTO read_range SELECT $1,$2,$3,$4::bigint+11+2*i,$4::bigint+12+2*i FROM generate_series($5::int,$6::int) i`, organization, channel, scenario.f.member.Member.ID, base, lo, lo+999)
			}
		} else {
			g.exec(`INSERT INTO read_range SELECT $1,$2,$3,$4::bigint+i*100+1,$4::bigint+(i+1)*100 FROM generate_series(1,$5::int-1) i`, organization, channel, scenario.f.member.Member.ID, base, topics/10)
		}
		g.exec(`INSERT INTO topic_read_floor (organization_id,channel_id,member_id,topic_id,floor_seq)
 SELECT $1,$2,$3,id,CASE WHEN $6 THEN $4::bigint+9 ELSE $4::bigint+i*10-CASE WHEN i%10=0 THEN 1 ELSE 0 END END
 FROM unnest($5::uuid[]) WITH ORDINALITY t(id,i)`, organization, channel, scenario.f.member.Member.ID, base, ids, heavy)
		g.exec(`UPDATE organization SET event_seq=greatest(event_seq,$2::bigint),event_log_boundary_seq=greatest(event_log_boundary_seq,$2::bigint) WHERE id=$1`, organization, cursor)
	}
	g.require(platform.InSnapshot(g.ctx, g.pool, func(s platform.Snapshot) error {
		var err error
		scenario.topics, err = unreadListBenchTopics(g.ctx, s, scenario.f)
		return err
	}))
	g.t.Logf("seed scenario=%s channels=%d topics/channel=%d sparse=every-tenth-topic heavy-channel=%t (100000 unread,10000 positive fragmented ranges); separate caught-up member", name, channels, topics, largest)
	g.check()
	return scenario
}
