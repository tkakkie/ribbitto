package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Posting owns the transaction that commits a message with its event.
type Posting struct {
	runner    TxRunner
	writer    WriterIn
	sequences EventSequenceIn
	events    EventAppenderIn
	notifier  Notifier
}

// NewPosting builds posting over transaction-bound factories. A nil notifier
// disables commit notifications, for example for seeding.
func NewPosting(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Posting {
	return &Posting{runner: runner, writer: writer, sequences: sequences, events: events, notifier: notifier}
}

// Post writes body to the channel's default topic. The organisation and author
// come from the membership; the channel is checked against that organisation.
func (p *Posting) Post(ctx context.Context, m org.Membership, channelID kernel.ID, body string) (Message, error) {
	return p.PostToTopic(ctx, m, channelID, nil, body)
}

// PostToTopic posts to a topic of the channel; nil selects its default topic.
func (p *Posting) PostToTopic(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, body string) (Message, error) {
	body, err := ValidateMessageBody(body)
	if err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrInvalidBody, err)
	}
	organizationID := m.Organization.ID
	var posted Message
	err = p.runner.InTx(ctx, func(tx platform.Tx) error {
		// The organisation lock must precede even the topic reads, so sequence
		// order remains commit order (decision 5).
		seq, err := p.sequences(tx).NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		writer := p.writer(tx)
		topic, err := writer.GetDefaultTopic(ctx, organizationID, channelID)
		// A channel always has a default topic; none means it is out of scope.
		if errors.Is(err, ErrTopicNotFound) {
			return ErrChannelNotFound
		}
		if err != nil {
			return err
		}
		if topicID != nil {
			topic, err = writer.GetTopic(ctx, organizationID, channelID, *topicID)
			if err != nil {
				return err
			}
		}
		posted, err = writer.InsertMessage(ctx, organizationID, channelID, topic.ID, m.Member.ID, body, seq)
		if err != nil {
			return err
		}
		return p.events(tx).Append(ctx, organizationID, seq, KindPosted, nil, EncodePosted(channelID, posted.ID, posted.TopicID))
	})
	if err != nil {
		return Message{}, fmt.Errorf("posting message: %w", err)
	}
	if p.notifier != nil {
		p.notifier.Raise(organizationID, posted.EventSeq)
	}
	return posted, nil
}
