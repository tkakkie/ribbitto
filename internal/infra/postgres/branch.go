package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// BranchStore implements topic.BranchStore: it owns the branching
// transaction.
type BranchStore struct {
	pool      *pgxpool.Pool
	sequences EventSequenceIn
	events    EventAppenderIn
}

// NewBranchStore returns a store using pool that takes org's sequences
// through sequences and appends events through events.
func NewBranchStore(pool *pgxpool.Pool, sequences EventSequenceIn, events EventAppenderIn) *BranchStore {
	return &BranchStore{pool: pool, sequences: sequences, events: events}
}

// Branch runs one branch atomically. Listed exceptions (feature map): it
// advances org's event_seq, writes message.topic_id, posts the notice into
// message and writes realtime's event_log, all in one transaction.
func (s *BranchStore) Branch(ctx context.Context, organizationID, channelID, memberID domain.ID, b topic.Branch, notice func(conversation.Topic) string) (conversation.Topic, int64, error) {
	var destination conversation.Topic
	var noticeSeq int64
	err := platform.InTx(ctx, s.pool, func(platformTx platform.Tx) error {
		tx := pgxbridge.Tx(platformTx)
		q := sqlcgen.New(tx)
		// Sequence first, as posting does: it locks the organisation's row,
		// and the move's sequence comes before the notice's.
		sequences := s.sequences(platformTx)
		moveSeq, err := sequences.NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		noticeSeq, err = sequences.NextEventSeq(ctx, organizationID)
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
			OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ChannelID: pgtype.UUID{Bytes: channelID, Valid: true},
			FromTopicID: pgtype.UUID{Bytes: source.ID, Valid: true}, ToTopicID: pgtype.UUID{Bytes: destination.ID, Valid: true},
			MessageIds: uuidArray(b.Messages),
		})
		if err != nil {
			return fmt.Errorf("moving messages: %w", err)
		}
		if moved != int64(len(b.Messages)) {
			return topic.ErrConflict // rolls back the new topic and both sequences
		}
		events := s.events(platformTx)
		data := conversation.EncodeMoved(conversation.Moved{ChannelID: channelID, FromTopicID: source.ID, ToTopicID: destination.ID, MessageIDs: b.Messages})
		if err := events.Append(ctx, organizationID, moveSeq, conversation.KindMessagesMoved, nil, data); err != nil {
			return err
		}
		posted, err := NewMessageStore(tx).InsertMessage(ctx, organizationID, channelID, source.ID, memberID, notice(destination), noticeSeq)
		if err != nil {
			return err
		}
		data = conversation.EncodePosted(channelID, posted.ID, posted.TopicID)
		return events.Append(ctx, organizationID, noticeSeq, conversation.KindPosted, nil, data)
	})
	if err != nil { // org.ErrNotFound from the sequence when the organisation is gone
		return conversation.Topic{}, 0, err
	}
	return destination, noticeSeq, nil
}
