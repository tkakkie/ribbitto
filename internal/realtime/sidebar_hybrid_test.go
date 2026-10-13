package realtime_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// hybridSnapshot reads its own repeatable-read state, never oracle results.
// Separate list queries and a grouped left join also avoid using the oracle's
// union/EXISTS algorithm. Fixtures are the cost harness's persisted fixtures.
func hybridSnapshot(ctx context.Context, pool *pgxpool.Pool, m org.Membership, channel kernel.ID, selected *kernel.ID) (sidebarOracleModel, error) {
	model := sidebarOracleModel{organization: m.Organization.ID, member: m.Member.ID, current: channel}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return model, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var joined int64
	if err := tx.QueryRow(ctx, `SELECT joined_event_seq FROM member WHERE organization_id=$1 AND id=$2`, m.Organization.ID, m.Member.ID).Scan(&joined); err != nil {
		return model, err
	}
	ch, topics := make(map[kernel.ID]int64), make(map[kernel.ID]int64)
	rows, err := tx.Query(ctx, `
SELECT m.channel_id, m.topic_id, count(*) FROM message m
LEFT JOIN read_range r ON r.organization_id=m.organization_id AND r.channel_id=m.channel_id
  AND r.member_id=$2 AND m.event_seq >= r.lo AND m.event_seq < r.hi
WHERE m.organization_id=$1 AND m.event_seq > $3 AND r.member_id IS NULL
GROUP BY m.channel_id, m.topic_id`, m.Organization.ID, m.Member.ID, joined)
	if err != nil {
		return model, err
	}
	defer rows.Close()
	for rows.Next() {
		var channelID, topic kernel.ID
		var n int64
		if err := rows.Scan(&channelID, &topic, &n); err != nil {
			return model, err
		}
		ch[channelID] += n
		topics[topic] = n
	}
	if err := rows.Err(); err != nil {
		return model, err
	}
	entry := func(id kernel.ID, name string, raw int64) sidebarOracleEntry {
		badge := ""
		if raw >= 100 {
			badge = "99+"
		} else if raw > 0 {
			badge = strconv.FormatInt(raw, 10)
		}
		return sidebarOracleEntry{id, name, sidebarOracleCount{raw, min(raw, 100), badge}}
	}
	list := func(query string, counts map[kernel.ID]int64, args ...any) ([]sidebarOracleEntry, error) {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var entries []sidebarOracleEntry
		for rows.Next() {
			var id kernel.ID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, err
			}
			entries = append(entries, entry(id, name, counts[id]))
		}
		return entries, rows.Err()
	}
	model.channels, err = list(`SELECT id,name FROM channel WHERE organization_id=$1 ORDER BY name,id`, ch, m.Organization.ID)
	if err != nil {
		return model, err
	}
	found := slices.ContainsFunc(model.channels, func(e sidebarOracleEntry) bool { return e.id == channel })
	if !found {
		return model, fmt.Errorf("hybrid channel outside organisation")
	}
	model.topics, err = list(`SELECT id,coalesce(name,'') FROM topic WHERE organization_id=$1 AND channel_id=$2 ORDER BY is_default DESC,lower(name),id LIMIT 50`, topics, m.Organization.ID, channel)
	if err != nil {
		return model, err
	}
	if selected != nil {
		model.selection, err = list(`SELECT id,coalesce(name,'') FROM topic WHERE organization_id=$1 AND channel_id=$2 AND id=$3`, topics, m.Organization.ID, channel, *selected)
		if err != nil || len(model.selection) != 1 {
			return model, fmt.Errorf("hybrid selection outside channel: %v", err)
		}
	}
	return model, tx.Commit(ctx)
}

type hybridRecord struct {
	condition, operation, mode, reason string
	request                            uint64 // Zero for an exact no-op delta.
}

type hybridRead struct{ organization, member kernel.ID } // Production invalidation only.

// hybridPrototype is a model writer driven by a controllable virtual clock.
// It deliberately makes no stream/HTML or performance claims. Each capture is
// owned by its request; the scheduler mailbox publishes it to the sole writer.
type hybridPrototype struct {
	condition          string
	registration       *realtime.RecountRegistration
	snapshot           func(context.Context) (sidebarOracleModel, error)
	state              sidebarOracleModel
	captures           map[uint64]*sidebarOracleModel
	requested, applied uint64
	interval           time.Duration
	next               time.Time
	records            []hybridRecord
}

func newHybrid(q *realtime.RecountScheduler, now time.Time, interval time.Duration, snapshot func(context.Context) (sidebarOracleModel, error)) *hybridPrototype {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &hybridPrototype{condition: "production-only", registration: q.Register(), snapshot: snapshot, captures: make(map[uint64]*sidebarOracleModel), interval: interval, next: now.Add(interval)}
}

func (p *hybridPrototype) reconcile(operation string) error {
	return p.reconcileBecause(operation, "fresh snapshot required")
}

func (p *hybridPrototype) reconcileBecause(operation, reason string) error {
	p.requested++
	capture := new(sidebarOracleModel)
	p.captures[p.requested] = capture
	if p.registration == nil || !p.registration.Submit(p.requested, func(ctx context.Context) (realtime.Outgoing, error) {
		var err error
		*capture, err = p.snapshot(ctx)
		return realtime.Outgoing{}, err
	}) {
		return realtime.ErrRecountRefused
	}
	p.records = append(p.records, hybridRecord{p.condition, operation, "reconciliation", reason, p.requested})
	return nil
}

func (p *hybridPrototype) handle(operation string, event *realtime.Event, read *hybridRead, organization, member kernel.ID) error {
	// There is no hypothetical metadata arm in 2a. Even an ordinary post in
	// another channel needs author/read effects; unknown kinds reconcile.
	noop := event != nil && event.OrganizationID != organization
	noop = noop || (read != nil && (read.organization != organization || read.member != member))
	if noop {
		p.records = append(p.records, hybridRecord{"production-only", operation, "delta-noop", "outside bound organisation or member", 0})
		return nil
	}
	reason := "unknown input or list metadata absent"
	switch {
	case read != nil:
		reason = "read commit and exact coverage absent"
	case event != nil && event.Kind == conversation.KindPosted:
		reason = "post author and page read effects absent"
	case event != nil && event.Kind == conversation.KindMessagesMoved:
		reason = "move read identities and list metadata absent"
	}
	return p.reconcileBecause(operation, reason)
}

func (p *hybridPrototype) tick(now time.Time) error {
	if now.Before(p.next) {
		return nil
	}
	for !p.next.After(now) {
		p.next = p.next.Add(p.interval)
	}
	return p.reconcileBecause("periodic", "unconditional lost-state repair")
}

func (p *hybridPrototype) complete() error {
	result, ok := <-p.registration.Completions()
	if !ok {
		return realtime.ErrRecountRefused
	}
	if result.Err != nil {
		return fmt.Errorf("hybrid recount: %w", result.Err)
	}
	if result.Version > p.applied {
		p.state, p.applied = *p.captures[result.Version], result.Version
	}
	// Coalesced requests have no completion; discard their captures too.
	for version := range p.captures {
		if version <= result.Version {
			delete(p.captures, version)
		}
	}
	return nil
}
