package realtime_test

import (
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

type counterKey struct{ member, channel kernel.ID }
type counterMessage struct {
	channel, topic kernel.ID
	seq, moved     int64
}
type counterMember struct {
	ID     kernel.ID
	Joined int64
}
type counterRange struct {
	Member, Channel kernel.ID
	Lo, Hi          int64
}
type counterTruth struct {
	messages []counterMessage
	members  []counterMember
	ranges   []counterRange
}

func counterQuery(t *testing.T, tx pgx.Tx, sql string, args ...any) pgx.Rows {
	t.Helper()
	rows, err := tx.Query(t.Context(), sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func counterLoad(t *testing.T, tx pgx.Tx, organization kernel.ID) counterTruth {
	t.Helper()
	messages, err := pgx.CollectRows(counterQuery(t, tx, `SELECT channel_id,topic_id,event_seq,coalesce(moved_event_seq,0) FROM message WHERE organization_id=$1`, organization), func(row pgx.CollectableRow) (counterMessage, error) {
		var m counterMessage
		err := row.Scan(&m.channel, &m.topic, &m.seq, &m.moved)
		return m, err
	})
	if err != nil {
		t.Fatal(err)
	}
	members, err := pgx.CollectRows(counterQuery(t, tx, `SELECT id,joined_event_seq FROM member WHERE organization_id=$1`, organization), pgx.RowToStructByPos[counterMember])
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := pgx.CollectRows(counterQuery(t, tx, `SELECT member_id,channel_id,lo,hi FROM read_range WHERE organization_id=$1`, organization), pgx.RowToStructByPos[counterRange])
	if err != nil {
		t.Fatal(err)
	}
	return counterTruth{messages, members, ranges}
}

func (s counterTruth) read(member kernel.ID, m counterMessage) bool {
	for _, u := range s.members {
		if u.ID == member && m.seq <= u.Joined {
			return true
		}
	}
	for _, r := range s.ranges {
		if r.Member == member && r.Channel == m.channel && r.Lo <= m.seq && m.seq < r.Hi {
			return true
		}
	}
	return false
}

// Count maps contain the channel and every nonempty topic, including off-list
// topics. A missing empty topic means zero; rebuilds never use capped counts.
type counterBundle struct {
	valid  bool
	counts map[kernel.ID]int64
}
type counterPrototype struct {
	totals    map[kernel.ID]map[kernel.ID]int64
	bundles   map[counterKey]*counterBundle
	suspended map[kernel.ID]bool
}

func counterRecount(s counterTruth, channel, member kernel.ID, reads bool) map[kernel.ID]int64 {
	result := map[kernel.ID]int64{}
	for _, m := range s.messages {
		if m.channel == channel && (!reads || s.read(member, m)) {
			result[channel]++
			result[m.topic]++
		}
	}
	return result
}

func (p *counterPrototype) materialize(s counterTruth, k counterKey) {
	p.bundles[k] = &counterBundle{true, counterRecount(s, k.channel, k.member, true)}
}

func (p *counterPrototype) advance(s counterTruth, k counterKey, topic kernel.ID, cursor int64) {
	b := p.bundles[k]
	if b == nil || !b.valid {
		return
	}
	for _, m := range s.messages {
		if m.channel == k.channel && m.seq <= cursor && (topic == (kernel.ID{}) || m.topic == topic && m.moved <= cursor) && !s.read(k.member, m) {
			b.counts[k.channel]++
			b.counts[m.topic]++
		}
	}
}

func (p *counterPrototype) post(s counterTruth, m counterMessage, author, scope kernel.ID, cursor int64) {
	k := counterKey{author, m.channel}
	p.advance(s, k, scope, cursor)
	p.totals[m.channel][m.channel]++
	p.totals[m.channel][m.topic]++
	if b := p.bundles[k]; b != nil && b.valid {
		b.counts[m.channel]++
		b.counts[m.topic]++
	}
}

func (p *counterPrototype) move(s counterTruth, channel, from, to kernel.ID, sequences ...int64) {
	for _, seq := range sequences {
		p.totals[channel][from]--
		p.totals[channel][to]++
		for k, b := range p.bundles {
			if k.channel == channel && b.valid && s.read(k.member, counterMessage{channel: channel, seq: seq}) {
				b.counts[from]--
				b.counts[to]++
			}
		}
	}
}

func counterEqual(a, b map[kernel.ID]int64) bool {
	// Zero rows and absent empty-topic rows represent the same complete bundle.
	for id, n := range a {
		if b[id] != n {
			return false
		}
	}
	for id, n := range b {
		if a[id] != n {
			return false
		}
	}
	return true
}

// Verification compares T and R separately: equal errors must not cancel in U.
// A shared-total mismatch suspends every member until current-truth repair.
func (p *counterPrototype) verify(s counterTruth, channel kernel.ID) bool {
	totals := counterRecount(s, channel, kernel.ID{}, false)
	totalOK := counterEqual(p.totals[channel], totals)
	ok := totalOK
	if !totalOK {
		p.suspended[channel] = true
	}
	for k, b := range p.bundles {
		if k.channel == channel && b.valid && (!totalOK || !counterEqual(b.counts, counterRecount(s, channel, k.member, true))) {
			b.valid = false
			ok = false
		}
	}
	return ok
}

func (p *counterPrototype) repair(s counterTruth, channel kernel.ID) {
	// Rebuild from the repair boundary, never from an earlier detection sample.
	p.totals[channel] = counterRecount(s, channel, kernel.ID{}, false)
	for k := range p.bundles {
		if k.channel == channel {
			p.materialize(s, k)
		}
	}
	delete(p.suspended, channel)
}

func counterDisplay(n int64) sidebarOracleCount {
	result := sidebarOracleCount{raw: n, capped: min(n, 100)}
	if n >= 100 {
		result.badge = "99+"
	} else if n > 0 {
		result.badge = strconv.FormatInt(n, 10)
	}
	return result
}

func (p *counterPrototype) model(t *testing.T, tx pgx.Tx, s counterTruth, organization, member, current kernel.ID, selected *kernel.ID) sidebarOracleModel {
	t.Helper()
	model := sidebarOracleModel{organization: organization, member: member, current: current}
	// Metadata is loaded independently; no oracle result feeds the model under test.
	rows := counterQuery(t, tx, `SELECT kind,id,name,channel FROM (
 SELECT 0 AS kind,id,name,id AS channel,false AS is_default FROM channel WHERE organization_id=$1
 UNION ALL SELECT 1,id,coalesce(name,''),channel_id,is_default FROM topic WHERE organization_id=$1 AND channel_id=$2
 ) metadata ORDER BY kind,is_default DESC,CASE WHEN kind=0 THEN name ELSE lower(name) END,id`, organization, current)
	defer rows.Close()
	for rows.Next() {
		var kind int
		var entry sidebarOracleEntry
		var channel kernel.ID
		if err := rows.Scan(&kind, &entry.id, &entry.name, &channel); err != nil {
			t.Fatal(err)
		}
		totals := p.totals[channel]
		b := p.bundles[counterKey{member, channel}]
		var reads map[kernel.ID]int64
		if b == nil || !b.valid || p.suspended[channel] {
			totals = counterRecount(s, channel, kernel.ID{}, false)
			reads = counterRecount(s, channel, member, true)
		} else {
			reads = b.counts
		}
		entry.count = counterDisplay(totals[entry.id] - reads[entry.id])
		if kind == 0 {
			model.channels = append(model.channels, entry)
		} else {
			if len(model.topics) < 50 {
				model.topics = append(model.topics, entry)
			}
			if selected != nil && entry.id == *selected {
				model.selection = []sidebarOracleEntry{entry}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return model
}
