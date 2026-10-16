package conversation

import (
	"context"
	"errors"
	"fmt"
	"math"

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
	reads     PostReadWriterIn
}

// NewPosting builds posting over transaction-bound factories. A nil notifier
// disables commit notifications, for example for seeding.
func NewPosting(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Posting {
	return &Posting{runner: runner, writer: writer, sequences: sequences, events: events, notifier: notifier}
}

// NewPagePosting builds posting with read writes bound to its transaction.
func NewPagePosting(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, reads PostReadWriterIn, notifier Notifier) *Posting {
	p := NewPosting(runner, writer, sequences, events, notifier)
	p.reads = reads
	return p
}

// Post writes body to the channel's default topic. The organisation and author
// come from the membership; the channel is checked against that organisation.
func (p *Posting) Post(ctx context.Context, m org.Membership, channelID kernel.ID, body string) (Message, error) {
	return p.PostToTopic(ctx, m, channelID, nil, body)
}

// PostToTopic posts to a topic of the channel; nil selects its default topic.
func (p *Posting) PostToTopic(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, body string) (Message, error) {
	sent, err := p.Send(ctx, m, channelID, Send{Topic: topicID, Body: body})
	return sent.Message, err
}

// PostFromPage reads the composer scope through its applied-and-shown cursor
// and the new message in the posting transaction.
func (p *Posting) PostFromPage(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, body string, cursor int64) (Message, error) {
	sent, err := p.Send(ctx, m, channelID, Send{Topic: topicID, Body: body, Cursor: &cursor})
	return sent.Message, err
}

// ErrInvalidPostCursor means the page cursor is invalid or ahead of committed events.
var ErrInvalidPostCursor = errors.New("invalid posting cursor")

// Send is one composer submission.
type Send struct {
	Topic *kernel.ID // A topic of the channel; nil is its default topic.
	Body  string
	// Cursor is the page's applied-and-shown cursor; nil reads only the new message.
	Cursor *int64
	// Key makes a repeated send return the first message; nil posts without it.
	Key *IdempotencyKey
}

// Sent is what a send produced.
type Sent struct {
	// Message is the new message or, on a repeat or with ErrPostKeyReused,
	// the author's stored one, wherever branching has moved it since.
	Message Message
	// Topic is the requested place: the topic resolved before the insert,
	// which a repeat's composer and typing stay on.
	Topic kernel.ID
	// Repeat reports that an earlier send with the key stored Message.
	Repeat bool
}

// Send posts a composer submission. A key the author already used returns
// that message without writing, or ErrPostKeyReused with it when the body
// or channel differs: the new text is not a retry and must not be dropped.
func (p *Posting) Send(ctx context.Context, m org.Membership, channelID kernel.ID, s Send) (Sent, error) {
	if s.Cursor != nil && (*s.Cursor < 0 || *s.Cursor >= math.MaxInt64-1) {
		return Sent{}, ErrInvalidPostCursor
	}
	body, err := ValidateMessageBody(s.Body)
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrInvalidBody, err)
	}
	topicID, cursor := s.Topic, s.Cursor
	organizationID := m.Organization.ID
	var posted Message
	var requested kernel.ID
	err = p.runner.InTx(ctx, func(tx platform.Tx) error {
		// The organisation lock must precede even the topic reads, so sequence
		// order remains commit order (decision 5). It also serialises two sends
		// with one key: the second sees the first's row in the unique index.
		seq, err := p.sequences(tx).NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		if cursor != nil && *cursor >= seq {
			return ErrInvalidPostCursor // seq - 1 is the committed limit under the organisation lock.
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
		requested = topic.ID
		posted, err = writer.InsertMessage(ctx, organizationID, channelID, topic.ID, m.Member.ID, body, seq, s.Key)
		if err != nil {
			return err
		}
		if p.reads != nil {
			previous, err := writer.LastMessageBefore(ctx, organizationID, channelID, seq)
			if err != nil {
				return err
			}
			var floor int64
			if cursor != nil {
				floor = *cursor
			}
			if topicID != nil && cursor != nil {
				intervening, err := writer.TopicChangedBetween(ctx, organizationID, channelID, *topicID, *cursor, seq)
				if err != nil {
					return err
				}
				if !intervening {
					floor = seq
				}
			}
			if err := p.reads(tx).Read(ctx, m, channelID, topicID, cursor, previous+1, seq+1, floor); err != nil {
				return err
			}
		}
		return p.events(tx).Append(ctx, organizationID, seq, KindPosted, nil, EncodePosted(channelID, posted.ID, posted.TopicID, m.Member.ID))
	})
	if errors.Is(err, ErrPostKeyTaken) && s.Key != nil {
		// The rollback returned the sequence (no holes); read the stored
		// message in a transaction of its own.
		return p.repeat(ctx, m, channelID, body, requested, *s.Key)
	}
	if err != nil {
		return Sent{}, fmt.Errorf("posting message: %w", err)
	}
	if p.notifier != nil {
		p.notifier.Raise(organizationID, posted.EventSeq)
	}
	return Sent{Message: posted, Topic: requested}, nil
}

// repeat answers a send whose key the author already stored. The topic is
// not compared: branching may have moved the message since.
func (p *Posting) repeat(ctx context.Context, m org.Membership, channelID kernel.ID, body string, requested kernel.ID, key IdempotencyKey) (Sent, error) {
	var stored Message
	err := p.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		stored, err = p.writer(tx).KeyedMessage(ctx, m.Organization.ID, m.Member.ID, key)
		return err
	})
	if err != nil {
		return Sent{}, fmt.Errorf("finding repeated message: %w", err)
	}
	sent := Sent{Message: stored, Topic: requested, Repeat: true}
	if stored.Body != body || stored.ChannelID != channelID {
		return sent, ErrPostKeyReused
	}
	return sent, nil
}
