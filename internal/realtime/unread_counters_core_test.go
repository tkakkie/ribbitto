package realtime_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

func TestUnreadCountersCore(t *testing.T) {
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "counters", "general")
	c, base := f.Channel.ID, f.Channel.DefaultTopicID
	other := conversationtest.Channel(t, pool, f.OrganizationID, "other", false)
	var selected kernel.ID
	for i := 1; i <= 50; i++ {
		selected = conversationtest.Topic(t, pool, f.OrganizationID, c, fmt.Sprintf("Topic%02d", i)).ID
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) SELECT $1,$2,$3,$4,'fixture',s FROM generate_series(1,106) s`, f.OrganizationID, c, base, f.MemberID)
	insert := func(channel, topic kernel.ID, seq int64) {
		exec(`INSERT INTO message (organization_id,channel_id,topic_id,member_id,body,event_seq) VALUES ($1,$2,$3,$4,'fixture',$5)`, f.OrganizationID, channel, topic, f.MemberID, seq)
	}
	insert(c, selected, 107)
	insert(other.ID, other.DefaultTopicID, 108)
	account := identitytest.Account(t, pool, "joined@example.org", "Joined")
	joined := orgtest.Member(t, pool, f.OrganizationID, account, org.RoleMember, "joined", 108)
	merge := func(member, channel kernel.ID, lo, hi int64) {
		t.Helper()
		if err := platform.InTx(t.Context(), pool, func(tx platform.Tx) error {
			return unreadpg.WriterIn(tx).Merge(t.Context(), unread.Scope{OrganizationID: f.OrganizationID, ChannelID: channel, MemberID: member}, 1, unread.Range{Lo: lo, Hi: hi})
		}); err != nil {
			t.Fatal(err)
		}
	}
	p := counterPrototype{totals: map[kernel.ID]map[kernel.ID]int64{}, bundles: map[counterKey]*counterBundle{}, suspended: map[kernel.ID]bool{}}
	var truth counterTruth
	owner := counterKey{f.MemberID, c}
	// No writer runs during these pinned snapshots. This checks an in-memory
	// maintenance model against persisted truth, not PostgreSQL lock correctness.
	check := func(name string, change func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			change()
			tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			truth = counterLoad(t, tx, f.OrganizationID)
			if len(p.totals) == 0 {
				p.totals[c] = counterRecount(truth, c, kernel.ID{}, false)
				p.totals[other.ID] = counterRecount(truth, other.ID, kernel.ID{}, false)
				p.materialize(truth, owner)
			}
			for _, member := range []kernel.ID{f.MemberID, joined} {
				want, err := sidebarOracleIn(t.Context(), tx, f.OrganizationID, member, c, &selected)
				if err != nil {
					t.Fatal(err)
				}
				if err := sidebarOracleCompare(p.model(t, tx, truth, f.OrganizationID, member, c, &selected), want); err != nil {
					t.Log(err)
					t.Fatal("independent full model differs from oracle")
				}
			}
		})
	}
	check("join-without-bundle-and-uncapped-unlisted-selection", func() {})
	check("topic-read", func() { p.advance(truth, owner, selected, 107); merge(f.MemberID, c, 107, 108) })
	check("post-with-implicit-feed-read", func() {
		p.post(truth, counterMessage{channel: c, topic: base, seq: 110}, f.MemberID, kernel.ID{}, 10)
		insert(c, base, 110)
		merge(f.MemberID, c, 0, 11)
		merge(f.MemberID, c, 110, 111)
	})
	check("cursorless-older-page-post", func() {
		p.post(truth, counterMessage{channel: c, topic: base, seq: 111}, f.MemberID, kernel.ID{}, 0)
		insert(c, base, 111)
		merge(f.MemberID, c, 111, 112)
	})
	check("feed-read", func() { p.advance(truth, owner, kernel.ID{}, 100); merge(f.MemberID, c, 0, 101) })
	move := func(from, to kernel.ID, seq, cursor int64) {
		exec(`UPDATE message SET topic_id=$1,moved_event_seq=$2 WHERE organization_id=$3 AND channel_id=$4 AND topic_id=$5 AND event_seq=$6`, to, cursor, f.OrganizationID, c, from, seq)
	}
	check("mixed-read-move-with-missing-bundle", func() {
		p.move(truth, c, base, selected, 10, 101)
		move(base, selected, 10, 112)
		move(base, selected, 101, 112)
	})
	check("branch-and-own-notice", func() {
		p.move(truth, c, selected, base, 101)
		move(selected, base, 101, 113)
		p.post(truth, counterMessage{channel: c, topic: selected, seq: 114}, f.MemberID, kernel.ID{}, 0)
		insert(c, selected, 114)
		merge(f.MemberID, c, 114, 115)
	})
	check("materialize-join-prefix-after-moves", func() { p.materialize(truth, counterKey{joined, c}) })
	check("invalid-bundle-fallback", func() { p.bundles[owner].valid = false })
	check("repair-member-bundle", func() { p.materialize(truth, owner) })
	check("detect-cancelling-total-and-read-drift", func() {
		p.totals[c][c]++
		p.bundles[owner].counts[c]++
		if p.verify(truth, c) || !p.suspended[c] || p.bundles[counterKey{joined, c}].valid {
			t.Fatal("shared total drift must suspend all bundles")
		}
	})
	check("post-while-suspended", func() {
		p.post(truth, counterMessage{channel: c, topic: base, seq: 115}, f.MemberID, kernel.ID{}, 0)
		insert(c, base, 115)
		merge(f.MemberID, c, 115, 116)
	})
	check("repair-current-truth", func() {
		p.repair(truth, c)
		if !p.verify(truth, c) || p.suspended[c] {
			t.Fatal("repair did not restore valid current counts")
		}
	})
	check("detect-read-drift", func() {
		p.bundles[owner].counts[selected]++
		if p.verify(truth, c) || p.bundles[owner].valid {
			t.Fatal("read drift must invalidate bundle")
		}
	})
	// Unbatched SQL model estimates; no query adapter or timing is measured.
	// M=2, D=2, Q=51; author valid for post, B=2 present bundles for repair.
	for _, a := range []int{0, 1, 2} {
		t.Logf("model estimate A=%d: post <=4 statements/4 rows; feed <=4/3; topic <=2/2; "+
			"move <=%d/%d; branch <=%d/%d; lazy join 0/0; materialize <=53/52; "+
			"channel repair <=159/156; fallback 1/0 (source writes and locks excluded)", a, 3+3*a, 2+2*a, 7+3*a, 6+2*a)
	}

}
