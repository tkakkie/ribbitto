package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// BranchStore implements topic.BranchStore: it owns the branching
// transaction.
type BranchStore struct {
	pool   *pgxpool.Pool
	events EventAppenders
}

// NewBranchStore returns a store using pool that appends events through
// events.
func NewBranchStore(pool *pgxpool.Pool, events EventAppenders) *BranchStore {
	return &BranchStore{pool: pool, events: events}
}

// Branch runs one branch atomically. Listed exceptions (feature map): it
// advances org's event_seq, writes message.topic_id, posts the notice into
// message and writes realtime's event_log, all in one transaction.
func (s *BranchStore) Branch(ctx context.Context, organizationID, channelID, memberID domain.ID, b topic.Branch, notice func(domain.Topic) string) (domain.Topic, int64, error) {
	var destination domain.Topic
	var noticeSeq int64
	err := platform.InTx(ctx, s.pool, func(platformTx platform.Tx) error {
		tx := pgxbridge.Tx(platformTx)
		q := sqlcgen.New(tx)
		org := pgtype.UUID{Bytes: organizationID, Valid: true}
		// Sequence first, as posting does: it locks the organisation's row,
		// and the move's sequence comes before the notice's.
		moveSeq, err := q.NextEventSeq(ctx, org)
		if err != nil {
			return err
		}
		noticeSeq, err = q.NextEventSeq(ctx, org)
		if err != nil {
			return err
		}
		topics := NewTopicStore(tx)
		source, err := topics.GetTopic(ctx, organizationID, channelID, b.From)
		if err != nil {
			return err
		}
		if b.To != nil {
			destination, err = topics.GetTopic(ctx, organizationID, channelID, *b.To)
		} else {
			destination, err = topics.CreateTopic(ctx, organizationID, channelID, b.NewName)
		}
		if err != nil {
			return err
		}
		moved, err := q.MoveMessages(ctx, sqlcgen.MoveMessagesParams{
			OrganizationID: org, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true},
			FromTopicID: pgtype.UUID{Bytes: source.ID, Valid: true}, ToTopicID: pgtype.UUID{Bytes: destination.ID, Valid: true},
			MessageIds: uuidArray(b.Messages),
		})
		if err != nil {
			return fmt.Errorf("moving messages: %w", err)
		}
		if moved != int64(len(b.Messages)) {
			return topic.ErrConflict // rolls back the new topic and both sequences
		}
		log := s.events(platformTx)
		if err := log.AppendMessagesMoved(ctx, organizationID, channelID, source.ID, destination.ID, b.Messages, moveSeq); err != nil {
			return err
		}
		posted, err := NewMessageStore(tx).InsertMessage(ctx, organizationID, channelID, source.ID, memberID, notice(destination), noticeSeq)
		if err != nil {
			return err
		}
		return log.AppendMessagePosted(ctx, organizationID, channelID, posted.ID, posted.TopicID, noticeSeq)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Topic{}, 0, authz.ErrNotFound // the organisation itself is gone
	}
	if err != nil {
		return domain.Topic{}, 0, err
	}
	return destination, noticeSeq, nil
}
