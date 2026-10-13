package realtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
)

func TestHybridHypotheticalDomain(t *testing.T) {
	for _, condition := range []string{"hypothetical-author", "hypothetical-read-ranges", "hypothetical-absolute-count"} {
		t.Run(condition, func(t *testing.T) { runHybridDomain(t, condition) })
	}
}

type hybridMessage struct {
	channel, topic kernel.ID
	seq            int64
	unread         bool
}

// This receipt is a test-only persisted publication, not production Reading.
// Revision identifies replayable committed coverage; delivery ordering is #961.
type hybridReceipt struct {
	organization, member, channel kernel.ID
	revision                      int64
	ranges                        []unread.Range
	absolute                      int64
}

type hybridHypothesis struct {
	condition string
	messages  []hybridMessage
}

func (h *hybridHypothesis) capture(ctx context.Context, pool *pgxpool.Pool, organization, member kernel.ID) error {
	rows, err := pool.Query(ctx, `SELECT m.channel_id,m.topic_id,m.event_seq,
 m.event_seq > u.joined_event_seq AND r.member_id IS NULL
FROM message m JOIN member u ON u.organization_id=m.organization_id AND u.id=$2
LEFT JOIN read_range r ON r.organization_id=m.organization_id AND r.channel_id=m.channel_id
 AND r.member_id=u.id AND m.event_seq>=r.lo AND m.event_seq<r.hi
WHERE m.organization_id=$1 ORDER BY m.event_seq`, organization, member)
	if err != nil {
		return err
	}
	defer rows.Close()
	h.messages = nil
	for rows.Next() {
		var m hybridMessage
		if err := rows.Scan(&m.channel, &m.topic, &m.seq, &m.unread); err != nil {
			return err
		}
		h.messages = append(h.messages, m)
	}
	return rows.Err()
}

// Delta display is computed independently of the oracle's display helper.
func hybridAdjust(entries []sidebarOracleEntry, id kernel.ID, delta int64, absolute bool) {
	for i := range entries {
		if entries[i].id != id {
			continue
		}
		n := entries[i].count.raw + delta
		if absolute {
			n = delta
		}
		badge := ""
		if n >= 100 {
			badge = "99+"
		} else if n > 0 {
			badge = strconv.FormatInt(n, 10)
		}
		entries[i].count = sidebarOracleCount{n, min(n, 100), badge}
	}
}

func (h *hybridHypothesis) handle(p *hybridPrototype, operation string, event *realtime.Event, read *hybridRead, author kernel.ID, receipt *hybridReceipt) error {
	mode, reason := "reconciliation", "unknown input or list metadata absent"
	noop := event != nil && event.OrganizationID != p.state.organization
	noop = noop || (read != nil && (read.organization != p.state.organization || read.member != p.state.member))
	switch {
	case noop:
		mode, reason = "delta-noop", "outside bound organisation or member"
	case event != nil && event.Kind == conversation.KindPosted:
		reason = "post author and page read effects absent"
		if h.condition == "hypothetical-author" && author != (kernel.ID{}) {
			reason = "author alone does not settle own read effects"
			if author != p.state.member {
				posted, err := conversation.DecodePosted(event.Payload)
				if err != nil || posted.TopicID == nil {
					return fmt.Errorf("hypothetical posted payload: %v", err)
				}
				hybridAdjust(p.state.channels, posted.ChannelID, 1, false)
				hybridAdjust(p.state.topics, *posted.TopicID, 1, false)
				hybridAdjust(p.state.selection, *posted.TopicID, 1, false)
				mode, reason = "delta", "committed non-author post with complete prior state"
			}
		}
	case read != nil:
		reason = "read commit and exact coverage absent"
		if receipt != nil && (h.condition != "hypothetical-read-ranges" || receipt.ranges != nil) && receipt.revision > 0 && receipt.organization == read.organization && receipt.member == read.member {
			switch h.condition {
			case "hypothetical-read-ranges":
				for i, m := range h.messages {
					if m.channel != receipt.channel || !m.unread {
						continue
					}
					for _, r := range receipt.ranges {
						if r.Lo <= m.seq && m.seq < r.Hi {
							hybridAdjust(p.state.channels, m.channel, -1, false)
							hybridAdjust(p.state.topics, m.topic, -1, false)
							hybridAdjust(p.state.selection, m.topic, -1, false)
							h.messages[i].unread = false
							break
						}
					}
				}
				mode, reason = "delta", "committed replayable coverage and cached message identities"
			case "hypothetical-absolute-count":
				reason = "absolute channel total cannot determine topics or selection"
				if receipt.channel != p.state.current {
					hybridAdjust(p.state.channels, receipt.channel, receipt.absolute, true)
					mode, reason = "delta", "transaction count for channel without topic or selection entries"
				}
			}
		}
	case event != nil && event.Kind == conversation.KindMessagesMoved:
		reason = "move read identities and list metadata absent"
	}
	if mode == "reconciliation" {
		if err := p.reconcileBecause(operation, reason); err != nil {
			return err
		}
		p.records[len(p.records)-1].condition = h.condition
	} else {
		p.records = append(p.records, hybridRecord{h.condition, operation, mode, reason, 0})
	}
	return nil
}

// Expectations depend on fixture operations, never on candidate routing/results.
func hybridHypotheticalRoute(condition, operation string, noop bool, ownPost bool) (string, string) {
	if noop {
		return "delta-noop", "outside bound organisation or member"
	}
	switch operation {
	case "cap-up-to-100", "cap-above-100", "older-page-own-post-only", "feed-page-post-reads-scope", "other-author-topic-post", "other-author-default-post", "topic-page-post-leaves-default-unread", "other-channel-post", "branch-notice-author-read":
		if condition == "hypothetical-author" {
			if !ownPost {
				return "delta", "committed non-author post with complete prior state"
			}
			return "reconciliation", "author alone does not settle own read effects"
		}
		return "reconciliation", "post author and page read effects absent"
	case "replayed-read-revision", "fragmented-read-cap-down", "overlapping-tab-read", "repeated-other-tab-read", "topic-through-latest-leaves-default-unread", "other-channel-feed-clears-badge", "other-member-persisted-read", "feed-after-topic-and-moves-clears-zero":
		if condition == "hypothetical-read-ranges" {
			return "delta", "committed replayable coverage and cached message identities"
		}
		if condition == "hypothetical-absolute-count" {
			if operation == "other-channel-feed-clears-badge" {
				return "delta", "transaction count for channel without topic or selection entries"
			}
			return "reconciliation", "absolute channel total cannot determine topics or selection"
		}
		return "reconciliation", "read commit and exact coverage absent"
	case "foreign-post":
		return "reconciliation", "post author and page read effects absent"
	case "foreign-read", "read-without-metadata", "unknown-read-outcome", "read-without-coverage":
		return "reconciliation", "read commit and exact coverage absent"
	case "move-read-and-unread":
		return "reconciliation", "move read identities and list metadata absent"
	}
	return "reconciliation", "unknown input or list metadata absent"
}

// Hypothetical fixture writes use SQL range union so metadata can share the
// raw transaction without importing the store-only pgxbridge. The production
// condition keeps its original WriterIn path; neither is a publisher test.
func hybridReadTransaction(ctx context.Context, pool *pgxpool.Pool, condition string, organization, member, channel kernel.ID, joined int64, ranges []unread.Range) (*hybridReceipt, error) {
	lo, hi := []int64{0}, []int64{joined + 1}
	for _, r := range ranges {
		lo, hi = append(lo, r.Lo), append(hi, r.Hi)
	}
	var revision int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO channel_read (organization_id,channel_id,member_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, organization, channel, member); err != nil {
			return err
		}
		var union []byte
		if err := tx.QueryRow(ctx, `WITH inputs AS (
SELECT int8range(lo,hi) r FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3
UNION ALL SELECT int8range(lo,hi) FROM unnest($4::bigint[],$5::bigint[]) x(lo,hi)
), merged AS (SELECT unnest(range_agg(r)) r FROM inputs)
SELECT json_agg(json_build_object('lo',lower(r),'hi',upper(r))) FROM merged`, organization, channel, member, lo, hi).Scan(&union); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM read_range WHERE organization_id=$1 AND channel_id=$2 AND member_id=$3`, organization, channel, member); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO read_range (organization_id,channel_id,member_id,lo,hi)
SELECT $1,$2,$3,lo,hi FROM json_to_recordset($4::json) x(lo bigint,hi bigint)`, organization, channel, member, union); err != nil {
			return err
		}
		var err error
		revision, err = hybridPersistReceipt(ctx, tx, condition, organization, member, channel)
		return err
	})
	if err != nil {
		return nil, err
	}
	return hybridReplayReceipt(ctx, pool, revision)
}

// Persist in the read transaction, then reload only after its successful commit.
// Each condition stores only its own metadata; the absolute arm pays for COUNT.
func hybridPersistReceipt(ctx context.Context, tx pgx.Tx, condition string, organization, member, channel kernel.ID) (int64, error) {
	var coverage []byte
	var absolute int64
	if condition == "hypothetical-read-ranges" {
		if err := tx.QueryRow(ctx, `SELECT coalesce(json_agg(json_build_object('Lo',lo,'Hi',hi) ORDER BY lo),'[]')
FROM read_range WHERE organization_id=$1 AND member_id=$2 AND channel_id=$3`, organization, member, channel).Scan(&coverage); err != nil {
			return 0, err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM message m JOIN member u ON u.organization_id=m.organization_id AND u.id=$2
LEFT JOIN read_range r ON r.organization_id=m.organization_id AND r.channel_id=m.channel_id AND r.member_id=u.id AND m.event_seq>=r.lo AND m.event_seq<r.hi
WHERE m.organization_id=$1 AND m.channel_id=$3 AND m.event_seq>u.joined_event_seq AND r.member_id IS NULL`, organization, member, channel).Scan(&absolute); err != nil {
			return 0, err
		}
	}
	var revision int64
	err := tx.QueryRow(ctx, `INSERT INTO hybrid_read_metadata (organization_id,member_id,channel_id,coverage,absolute_count)
VALUES ($1,$2,$3,$4,$5) RETURNING revision`, organization, member, channel, coverage, absolute).Scan(&revision)
	return revision, err
}

func hybridReplayReceipt(ctx context.Context, pool *pgxpool.Pool, revision int64) (*hybridReceipt, error) {
	r := &hybridReceipt{revision: revision}
	var coverage []byte
	if err := pool.QueryRow(ctx, `SELECT organization_id,member_id,channel_id,coverage,absolute_count
FROM hybrid_read_metadata WHERE revision=$1`, revision).Scan(&r.organization, &r.member, &r.channel, &coverage, &r.absolute); err != nil {
		return nil, err
	}
	if len(coverage) > 0 {
		if err := json.Unmarshal(coverage, &r.ranges); err != nil {
			return nil, err
		}
	}
	return r, nil
}
