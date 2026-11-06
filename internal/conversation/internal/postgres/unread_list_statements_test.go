package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// These drafts deliberately stay in tests. V0 crosses conversation/unread's
// table boundary only to measure a reference; it is not for production (#2191).
// V1 transfers unread-owned state to a conversation-only statement.
const unreadListStateSQL = `-- name: OrgReadState :many
SELECT r.channel_id,0 AS kind,r.lo AS a,r.hi AS b,NULL::uuid AS topic_id
FROM read_range r WHERE r.organization_id=$1 AND r.member_id=$2 AND r.channel_id=ANY($3::uuid[])
UNION ALL
SELECT f.channel_id,1,f.floor_seq,0,f.topic_id FROM topic_read_floor f
WHERE f.organization_id=$1 AND f.member_id=$2 AND f.channel_id=ANY($3::uuid[])
ORDER BY 1,2,3`

type unreadListRow struct {
	Channel, Topic, FirstID kernel.ID
	Name                    string
	Default, Resolved       bool
	Last, Count, First      int64
}

type unreadListPosition struct {
	Seq   int64
	Topic kernel.ID
}

// mutation is test-local: every mutation must be rejected by the oracle gate.
func unreadListSQL(form, mutation string) string {
	channels := `SELECT c.id AS channel_id,coalesce((SELECT r.hi FROM read_range r
 WHERE r.organization_id=$1 AND r.channel_id=c.id AND r.member_id=$2 AND r.lo=0),$3::bigint+1) AS p
 FROM unnest($4::uuid[]) c(id)`
	floor := `LEFT JOIN topic_read_floor f ON f.organization_id=$1 AND f.channel_id=t.channel_id AND f.member_id=$2 AND f.topic_id=t.id`
	notRead := `NOT EXISTS (SELECT 1 FROM (SELECT r.hi FROM read_range r
 WHERE r.organization_id=$1 AND r.channel_id=c.channel_id AND r.member_id=$2 AND r.lo<=m.event_seq
 ORDER BY r.lo DESC LIMIT 1) pr WHERE pr.hi>m.event_seq)`
	if mutation == "drop-positive-ranges" {
		notRead = strings.Replace(notRead, "AND r.lo<=m.event_seq", "AND r.lo=0 AND r.lo<=m.event_seq", 1)
	}
	if form == "V1" {
		channels = `SELECT channel_id,p,read_set FROM unnest($4::uuid[],$7::bigint[],$8::int8multirange[]) c(channel_id,p,read_set)`
		floor = `LEFT JOIN unnest($9::uuid[],$10::bigint[]) f(topic_id,floor_seq) ON f.topic_id=t.id`
		notRead = `NOT (c.read_set @> m.event_seq)`
	}
	floorValue := "coalesce(f.floor_seq,l.p-1)"
	if mutation == "ignore-explicit-floor" {
		floorValue = "l.p-1"
	}
	if mutation == "ignore-join-prefix" && form == "V0" {
		channels = strings.Replace(channels, "$3::bigint+1", "0::bigint", 1)
	}
	scope := `m.organization_id=$1 AND m.channel_id=c.channel_id AND m.topic_id=c.topic_id AND m.event_seq>=c.p AND ` + notRead
	if mutation == "exclusive-prefix" {
		scope = strings.Replace(scope, "m.event_seq>=c.p", "m.event_seq>c.p", 1)
	}
	above := `SELECT m.event_seq FROM message m WHERE ` + scope + ` AND m.event_seq>c.floor ORDER BY m.event_seq`
	movedWhere := scope + ` AND c.explicit_floor IS NOT NULL AND m.event_seq<=c.floor AND m.moved_event_seq>c.floor`
	if mutation == "inclusive-floor" {
		above = strings.Replace(above, "m.event_seq>c.floor", "m.event_seq>=c.floor", 1)
	}
	if mutation == "inclusive-move-floor" {
		movedWhere = strings.Replace(movedWhere, "m.moved_event_seq>c.floor", "m.moved_event_seq>=c.floor", 1)
	}
	if mutation == "drop-moved-branch" {
		movedWhere += " AND false"
	}
	moved := `SELECT m.event_seq FROM message m WHERE ` + movedWhere
	capped := `SELECT event_seq FROM ((` + above + ` LIMIT 100) UNION ALL (` + moved + ` ORDER BY m.moved_event_seq LIMIT 100)) branches ORDER BY event_seq LIMIT 100`
	if mutation == "cap-101" {
		capped = strings.Replace(capped, "branches ORDER BY event_seq LIMIT 100", "branches ORDER BY event_seq LIMIT 101", 1)
	}
	firstMoved := moved
	if mutation == "capped-first-unread" {
		firstMoved += " ORDER BY m.moved_event_seq LIMIT 100"
	}
	extra := ""
	if mutation == "sidebar-50" {
		extra = ` AND t.id IN (SELECT id FROM topic WHERE organization_id=$1 AND channel_id=t.channel_id ORDER BY is_default DESC,(conclusion_message_id IS NOT NULL),lower(name),id LIMIT 50)`
	}
	discovery := ""
	if mutation == "channel-first-100" {
		discovery = ` AND EXISTS (SELECT 1 FROM (SELECT m.topic_id FROM message m WHERE m.organization_id=$1 AND m.channel_id=c.channel_id AND m.event_seq>=c.p AND ` + notRead + ` ORDER BY m.event_seq LIMIT 100) bounded WHERE bounded.topic_id=c.topic_id)`
	}
	keyset := `($5::bigint IS NULL OR (nl.last_seq,t.id)<($5,$6::uuid))`
	if mutation == "inclusive-keyset" {
		keyset = strings.Replace(keyset, "<($5", "<=($5", 1)
	}
	return `-- name: UnreadTopicPage` + form + ` :many
WITH scope AS (SELECT $1::uuid,$2::uuid,$3::bigint),chan AS (` + channels + `),live AS (
 SELECT * FROM chan WHERE EXISTS (SELECT 1 FROM message m WHERE m.organization_id=$1 AND m.channel_id=chan.channel_id AND m.event_seq>=chan.p)
),cand AS (
 SELECT t.id AS topic_id,l.*,nl.last_seq,f.floor_seq AS explicit_floor,` + floorValue + ` AS floor
 FROM live l JOIN topic t ON t.organization_id=$1 AND t.channel_id=l.channel_id
 CROSS JOIN LATERAL (SELECT m.event_seq AS last_seq FROM message m WHERE m.organization_id=$1 AND m.topic_id=t.id ORDER BY m.event_seq DESC LIMIT 1) nl
 ` + floor + ` WHERE nl.last_seq>=l.p AND ` + keyset + extra + `
),hit AS (
 SELECT c.* FROM cand c WHERE (EXISTS (` + above + `) OR EXISTS (` + moved + `))` + discovery + `
 ORDER BY c.last_seq DESC,c.topic_id DESC LIMIT 51
)
SELECT c.channel_id,c.topic_id,coalesce(t.name,''),t.is_default,t.conclusion_message_id IS NOT NULL,c.last_seq,cnt.n,fm.id,first.seq
FROM hit c JOIN topic t ON t.organization_id=$1 AND t.id=c.topic_id
CROSS JOIN LATERAL (SELECT count(*) n FROM (` + capped + `) capped) cnt
CROSS JOIN LATERAL (
 WITH moved AS MATERIALIZED (` + firstMoved + `)
 SELECT min(event_seq) seq FROM ((` + above + ` LIMIT 1) UNION ALL (SELECT event_seq FROM moved)) candidates
) first
JOIN message fm ON fm.organization_id=$1 AND fm.event_seq=first.seq
ORDER BY c.last_seq DESC,c.topic_id DESC`
}

func unreadListPage(ctx context.Context, s platform.Snapshot, m org.Membership, readable []kernel.ID, after *unreadListPosition, form, mutation string) ([]unreadListRow, error) {
	tx := pgxbridge.Snapshot(s)
	ids := make([]kernel.ID, 0, len(readable))
	seen := make(map[kernel.ID]bool, len(readable))
	for _, id := range readable {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	var seq, topic any
	if after != nil {
		seq, topic = after.Seq, after.Topic
	}
	args := []any{m.Organization.ID, m.Member.ID, m.Member.JoinedEventSeq, ids, seq, topic}
	if form == "V1" {
		prefixes, sets, topics, floors, err := unreadListState(ctx, tx, m, ids, mutation)
		if err != nil {
			return nil, err
		}
		args = append(args, prefixes, sets, topics, floors)
	}
	rows, err := tx.Query(ctx, unreadListSQL(form, mutation), args...)
	if err != nil {
		return nil, fmt.Errorf("querying %s list: %w", form, err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (unreadListRow, error) {
		var r unreadListRow
		err := row.Scan(&r.Channel, &r.Topic, &r.Name, &r.Default, &r.Resolved, &r.Last, &r.Count, &r.FirstID, &r.First)
		return r, err
	})
}

func unreadListState(ctx context.Context, tx pgx.Tx, m org.Membership, ids []kernel.ID, mutation string) ([]int64, []pgtype.Multirange[pgtype.Range[pgtype.Int8]], []kernel.ID, []int64, error) {
	rows, err := tx.Query(ctx, unreadListStateSQL, m.Organization.ID, m.Member.ID, ids)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading organization read state: %w", err)
	}
	defer rows.Close()
	prefixes := make([]int64, len(ids))
	sets := make([]pgtype.Multirange[pgtype.Range[pgtype.Int8]], len(ids))
	positions := make(map[kernel.ID]int, len(ids))
	for i, id := range ids {
		positions[id] = i
		prefixes[i] = m.Member.JoinedEventSeq + 1
		if mutation == "ignore-join-prefix" {
			prefixes[i] = 0
		}
	}
	var topics []kernel.ID
	var floors []int64
	for rows.Next() {
		var channel kernel.ID
		var kind int32
		var a, b int64
		var topic pgtype.UUID
		if err := rows.Scan(&channel, &kind, &a, &b, &topic); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("decoding organization read state: %w", err)
		}
		i := positions[channel]
		if kind == 1 {
			topics = append(topics, topic.Bytes)
			floors = append(floors, a)
			continue
		}
		if a == 0 {
			prefixes[i] = b
		} else if mutation != "drop-positive-ranges" {
			sets[i] = append(sets[i], unreadListRange(a, b))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("reading organization read state: %w", err)
	}
	for i, p := range prefixes {
		if p > 0 {
			sets[i] = append(pgtype.Multirange[pgtype.Range[pgtype.Int8]]{unreadListRange(0, p)}, sets[i]...)
		}
	}
	// Nil arrays must be empty arrays, not SQL NULL, when there are no floors.
	return prefixes, sets, slices.Clip(append([]kernel.ID{}, topics...)), append([]int64{}, floors...), nil
}

func unreadListRange(lo, hi int64) pgtype.Range[pgtype.Int8] {
	return pgtype.Range[pgtype.Int8]{Lower: pgtype.Int8{Int64: lo, Valid: true}, Upper: pgtype.Int8{Int64: hi, Valid: true}, LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}
}
