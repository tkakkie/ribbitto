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
	snapshots SnapshotRunner
	members   MemberDirectoryIn
	quotes    ReadStoreIn
}

// NewPosting builds posting over transaction-bound factories. A nil notifier
// disables commit notifications, for example for seeding.
func NewPosting(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Posting {
	return &Posting{runner: runner, writer: writer, sequences: sequences, events: events, notifier: notifier}
}

// NewPagePosting builds posting with read writes bound to its transaction,
// and member references and quoted messages read through members and
// quotes in a snapshot of their own. Posting built without members refuses
// every body with a reference or handle candidate, and without quotes every quote.
func NewPagePosting(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, reads PostReadWriterIn, snapshots SnapshotRunner, members MemberDirectoryIn, quotes ReadStoreIn, notifier Notifier) *Posting {
	p := NewPosting(runner, writer, sequences, events, notifier)
	p.reads, p.snapshots, p.members, p.quotes = reads, snapshots, members, quotes
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

// ErrUnknownReference wraps ErrInvalidBody for a body referencing a member
// the URL's organisation's directory did not return. A missing and another
// organisation's member are the same error, so the answer reveals nothing
// about other organisations (decision 39).
var ErrUnknownReference = errors.New("message references an unknown member")

// ErrInvalidPostCursor means the page cursor is invalid or ahead of committed events.
var ErrInvalidPostCursor = errors.New("invalid posting cursor")

// Send is one composer submission.
type Send struct {
	Topic *kernel.ID // A topic of the channel; nil is its default topic.
	Body  string
	// Cursor is validated but not used for reading; kept for old pages until S5.
	Cursor *int64
	// Key makes a repeated send return the first message; nil posts without it.
	Key *IdempotencyKey
	// Quote is a message of the channel to quote (decision 38), or nil.
	Quote *kernel.ID
	// QuoteText selects a part; nil copies the whole extracted body.
	QuoteText *string
	// UnknownMember stands in the quote's text for a member the quoted
	// chat_v1 message references and the directory did not return.
	UnknownMember string
}

// Sent is what a send produced.
type Sent struct {
	// Message is the new message or, on a repeat or with ErrPostKeyReused,
	// the author's stored one, wherever branching has moved it since.
	Message Message
	// Topic is the requested place, resolved before inserting or replaying,
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
	// A replay is compared with typed text before resolving today's handles
	// or checking expansion. The unique index still decides concurrent sends.
	tokens := handleTokens(body)
	if s.Key != nil && p.members != nil && len(tokens) > 0 {
		sent, err := p.repeat(ctx, m, channelID, body, s, kernel.ID{}, *s.Key)
		if !errors.Is(err, ErrMessageNotFound) {
			return sent, err
		}
	}
	typed := body
	var proof TypedBodyProof
	if s.Key != nil {
		proof = proveTypedBody(typed)
	}
	if body, err = p.convertHandles(ctx, m.Organization.ID, body, tokens); err != nil {
		return Sent{}, err
	}
	if body, err = ValidateMessageBody(body); err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrInvalidBody, err)
	}
	if err := p.checkReferences(ctx, m.Organization.ID, body); err != nil {
		return Sent{}, err
	}
	var quoted map[kernel.ID]Reference
	if s.Quote != nil {
		if quoted, err = p.quoteReferences(ctx, m.Organization.ID, channelID, *s.Quote); err != nil {
			return Sent{}, err
		}
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
		var quote *Quote
		if s.Quote != nil {
			// Read under the organisation lock, which moves take too, so the
			// original is still in this channel when the quote is inserted.
			original, err := writer.QuotedMessage(ctx, organizationID, channelID, *s.Quote)
			if err != nil {
				return err
			}
			if quote, err = quoteOf(original, quoted, s.UnknownMember, s.QuoteText); err != nil {
				return err
			}
		}
		posted, err = writer.InsertMessage(ctx, organizationID, channelID, topic.ID, m.Member.ID, body, seq, s.Key, proof, quote)
		if err != nil {
			return err
		}
		if p.reads != nil {
			previous, err := writer.LastMessageBefore(ctx, organizationID, channelID, seq)
			if err != nil {
				return err
			}
			lo := previous + 1
			if cursor == nil {
				lo = previous
			}
			if err := p.reads(tx).Read(ctx, m, channelID, lo, seq+1); err != nil {
				return err
			}
		}
		return p.events(tx).Append(ctx, organizationID, seq, KindPosted, nil, EncodePosted(channelID, posted.ID, posted.TopicID, m.Member.ID))
	})
	if errors.Is(err, ErrPostKeyTaken) && s.Key != nil {
		// The rollback returned the sequence (no holes); read the stored
		// message in a transaction of its own.
		return p.repeat(ctx, m, channelID, typed, s, requested, *s.Key)
	}
	if err != nil {
		return Sent{}, fmt.Errorf("posting message: %w", err)
	}
	if p.notifier != nil {
		p.notifier.Raise(organizationID, posted.EventSeq)
	}
	return Sent{Message: posted, Topic: requested}, nil
}

// checkReferences looks every member reference in body up in one batch in
// the organisation's directory, before and outside the posting transaction,
// so the organisation lock is never held for it. A member who leaves between
// the check and the insert is shown as the placeholder, like any member who
// leaves later (decision 39).
func (p *Posting) checkReferences(ctx context.Context, organizationID kernel.ID, body string) error {
	ids := appendReferences(nil, ParseChatV1(body))
	if len(ids) == 0 {
		return nil
	}
	if p.members == nil {
		return errors.New("posting has no member directory to check references")
	}
	var found map[kernel.ID]org.DirectoryEntry
	err := p.snapshots.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		var err error
		found, err = p.members(snapshot).LookupMembers(ctx, organizationID, ids)
		return err
	})
	if err != nil {
		return fmt.Errorf("checking member references: %w", err)
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("%w: %w", ErrInvalidBody, ErrUnknownReference)
		}
	}
	return nil
}

// repeat answers a send whose key the author already stored. The topic is
// not compared: branching may have moved the message since.
func (p *Posting) repeat(ctx context.Context, m org.Membership, channelID kernel.ID, body string, s Send, requested kernel.ID, key IdempotencyKey) (Sent, error) {
	var stored Message
	var original Message
	err := p.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		stored, err = p.writer(tx).KeyedMessage(ctx, m.Organization.ID, m.Member.ID, key)
		if err != nil {
			return err
		}
		if requested == (kernel.ID{}) {
			// This is a replay-only read transaction, with no sequence or writes.
			// Keep the requested composer scope even if the message moved.
			topic, lookupErr := p.writer(tx).GetDefaultTopic(ctx, m.Organization.ID, channelID)
			if errors.Is(lookupErr, ErrTopicNotFound) {
				return ErrChannelNotFound
			}
			if lookupErr != nil {
				return lookupErr
			}
			if s.Topic != nil {
				topic, lookupErr = p.writer(tx).GetTopic(ctx, m.Organization.ID, channelID, *s.Topic)
				if lookupErr != nil {
					return lookupErr
				}
			}
			requested = topic.ID
		}
		// A missing key must not change fresh-send validation precedence.
		// Found keys resolve scope before part validity and key comparison.
		if s.Quote != nil {
			original, err = p.writer(tx).QuotedMessage(ctx, m.Organization.ID, channelID, *s.Quote)
		}
		return err
	})
	if err != nil {
		return Sent{}, fmt.Errorf("finding repeated message: %w", err)
	}
	var quote *Quote
	if s.Quote != nil {
		references, err := p.quoteReferences(ctx, m.Organization.ID, channelID, *s.Quote)
		if err != nil {
			return Sent{}, err
		}
		if quote, err = quoteOf(original, references, s.UnknownMember, s.QuoteText); err != nil {
			return Sent{}, err
		}
	}
	sent := Sent{Message: stored, Topic: requested, Repeat: true}
	if !stored.sameTypedBody(body) || stored.ChannelID != channelID || !sameQuote(stored.Quote, s.Quote) {
		return sent, ErrPostKeyReused
	}
	// A whole and a part equal to the whole are the same snapshot.
	if quote != nil && stored.Quote.Text != quote.Text {
		return sent, ErrPostKeyReused
	}
	return sent, nil
}

// sameQuote reports whether a stored message quotes the requested original.
func sameQuote(stored *Quote, requested *kernel.ID) bool {
	if stored == nil || requested == nil {
		return stored == nil && requested == nil
	}
	return stored.MessageID != nil && *stored.MessageID == *requested
}

// quoteReferences reads the original in a snapshot of its own, before the
// posting transaction, to resolve the members a chat_v1 original references,
// so the organisation lock never waits on the directory. It decides nothing:
// a missing original has no references, and the scoped read in the
// transaction refuses it.
func (p *Posting) quoteReferences(ctx context.Context, organizationID, channelID, id kernel.ID) (map[kernel.ID]Reference, error) {
	if p.quotes == nil {
		return nil, errors.New("posting has no reader to check quotes")
	}
	var references map[kernel.ID]Reference
	err := p.snapshots.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		found, err := p.quotes(snapshot).GetMessages(ctx, organizationID, channelID, []kernel.ID{id})
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return nil
		}
		blocks := ParseChatV1(found[0].Body)
		ids := appendReferences(nil, blocks)
		if found[0].Format != FormatChatV1 || len(ids) == 0 {
			return nil
		}
		if p.members == nil {
			return errors.New("posting has no member directory to resolve a quote")
		}
		members, err := p.members(snapshot).LookupMembers(ctx, organizationID, ids)
		references = resolveReferences(blocks, members, nil)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading quoted message: %w", err)
	}
	return references, nil
}

// quoteOf copies the original's own text, never its quote (decision 38): a
// plain_text body as stored, a chat_v1 body's extraction with references
// resolved at quote time. The text must pass a body's checks after trimming,
// so an extraction with nothing left is refused like an invalid body.
func quoteOf(original Message, references map[kernel.ID]Reference, unknown string, part *string) (*Quote, error) {
	extracted := Entry{Message: original, References: references}.Text(unknown)
	text, err := ValidateMessageBody(extracted)
	if part != nil {
		text, err = ValidateQuotePart(*part, extracted)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: quoted text: %w", ErrInvalidBody, err)
	}
	return &Quote{MessageID: &original.ID, MemberID: original.MemberID, Text: text}, nil
}
