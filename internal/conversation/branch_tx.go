package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func (s *Brancher) run(ctx context.Context, organizationID, channelID, memberID kernel.ID, b Branch, notice func(Topic) string) (Topic, int64, error) {
	var destination Topic
	var noticeSeq int64
	err := s.runner.InTx(ctx, func(tx platform.Tx) error {
		// Lock the organisation before any topic read or write; the move's
		// sequence must precede the notice's (decision 5).
		sequences := s.sequences(tx)
		moveSeq, err := sequences.NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		noticeSeq, err = sequences.NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		writer := s.writer(tx)
		source, err := writer.GetTopic(ctx, organizationID, channelID, b.From)
		if err != nil {
			return err
		}
		if b.To != nil {
			destination, err = writer.GetTopic(ctx, organizationID, channelID, *b.To)
		} else {
			destination, err = writer.CreateTopic(ctx, organizationID, channelID, b.NewName)
		}
		if err != nil {
			return err
		}
		moved, err := writer.MoveMessages(ctx, organizationID, channelID, source.ID, destination.ID, b.Messages, moveSeq)
		if err != nil {
			return err
		}
		if moved != int64(len(b.Messages)) {
			return ErrBranchConflict // rolls back the new topic, moved rows and both sequences
		}
		events := s.events(tx)
		data := EncodeMoved(Moved{ChannelID: channelID, FromTopicID: source.ID, ToTopicID: destination.ID, MessageIDs: b.Messages})
		if err := events.Append(ctx, organizationID, moveSeq, KindMessagesMoved, nil, data); err != nil {
			return err
		}
		// InsertNotice deliberately maps no errors: a failed notice stays a
		// server error, unlike posting's mapped insert (R2 on #502).
		posted, err := writer.InsertNotice(ctx, organizationID, channelID, source.ID, memberID, notice(destination), noticeSeq)
		if err != nil {
			return err
		}
		return events.Append(ctx, organizationID, noticeSeq, KindPosted, nil, EncodePosted(channelID, posted.ID, posted.TopicID))
	})
	return destination, noticeSeq, err
}
