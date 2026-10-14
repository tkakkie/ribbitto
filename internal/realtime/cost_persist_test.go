package realtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

type costPersistKey struct{}
type costCommitKey struct{}
type costPersist struct{ at time.Time }

// pgx invokes TraceQueryEnd after the server's COMMIT reply, before Commit
// returns. This is a client-observed persistence boundary, not server wall time.
// WithoutCancel preserves this pointer through Reading's detached commit.
func costPersistEnd(ctx context.Context, data pgx.TraceQueryEndData) {
	p, _ := ctx.Value(costPersistKey{}).(*costPersist)
	if p != nil && ctx.Value(costCommitKey{}) == true && data.Err == nil && data.CommandTag.String() == "COMMIT" {
		p.at = time.Now()
	}
}

type costChange struct {
	stream                int // -1 means a post affects every stream.
	sequence, readThrough int64
	persisted             time.Time
}

func (r *costRunner) persisted(change costChange, p *costPersist) {
	change.persisted = p.at // Missing commit evidence remains visible for #952c2b.
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.changesPersisted = append(r.changesPersisted, change)
}

func (r *costRunner) post(ctx context.Context, posting *conversation.Posting, m org.Membership, channel kernel.ID) (conversation.Message, error) {
	p := new(costPersist)
	posted, err := posting.Post(context.WithValue(ctx, costPersistKey{}, p), m, channel, "cost")
	if err == nil {
		r.persisted(costChange{stream: -1, sequence: posted.EventSeq}, p)
	}
	return posted, err
}

// Keep the first sends, so overflow cannot overwrite the first exact delivery.
// #952c2b must treat a window with dropped evidence as incomplete, never a pass.
const costSentLimit = 1024

// The digest covers the full model, but retains no model slices or strings.
// elapsed preserves sender time relative to the connection's monotonic anchor.
type costSentModel struct {
	elapsed               time.Duration
	sequence, readThrough int64
	digest                [sha256.Size]byte
	kind                  costExactKind
}

// Caller holds the connection lock, as when installing the successful model.
func (c *costModelConnection) sentModel(frame hybridFrame) {
	at := time.Now() // After underlying sender success, before digest work.
	if len(c.sent) == costSentLimit {
		c.sentDropped++
		return
	}
	if c.sent == nil {
		c.sentStart = at
		c.sent = make([]costSentModel, 0, costSentLimit)
	}
	c.sent = append(c.sent, costSentModel{
		elapsed: at.Sub(c.sentStart), sequence: frame.boundary.sequence,
		readThrough: frame.readThrough, digest: costModelDigest(frame.model), kind: frame.exactKind,
	})
}

// Length prefixes preserve list boundaries and arbitrary names/badges; order,
// scope, selection and uncapped/display counts all contribute to exactness.
// No oracle SQL runs here: #952c2b can hash its independent expected model.
func costModelDigest(model sidebarOracleModel) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write(model.organization[:])
	_, _ = h.Write(model.member[:])
	_, _ = h.Write(model.current[:])
	number := func(n int64) {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(n))
		_, _ = h.Write(buf[:])
	}
	str := func(s string) {
		number(int64(len(s)))
		_, _ = h.Write([]byte(s))
	}
	for _, entries := range [][]sidebarOracleEntry{model.channels, model.topics, model.selection} {
		number(int64(len(entries)))
		for _, entry := range entries {
			_, _ = h.Write(entry.id[:])
			str(entry.name)
			number(entry.count.raw)
			number(entry.count.capped)
			str(entry.count.badge)
		}
	}
	var digest [sha256.Size]byte
	h.Sum(digest[:0])
	return digest
}
