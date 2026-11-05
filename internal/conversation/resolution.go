package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrDefaultTopicResolution refuses a conclusion on the default topic (HTTP 422).
var ErrDefaultTopicResolution = errors.New("default topic cannot be resolved")

// ErrConclusionConflict means the chosen message left the topic (HTTP 409).
var ErrConclusionConflict = errors.New("conclusion message is no longer in the topic")

// Resolution commits a topic's conclusion with its durable invalidation.
type Resolution struct {
	runner    TxRunner
	writer    WriterIn
	sequences EventSequenceIn
	events    EventAppenderIn
	notifier  Notifier
}

// NewResolution builds resolution over transaction-bound factories; notifier may be nil.
func NewResolution(runner TxRunner, writer WriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Resolution {
	return &Resolution{runner, writer, sequences, events, notifier}
}

// Resolve sets or replaces a named topic's conclusion with a current message.
func (r *Resolution) Resolve(ctx context.Context, m org.Membership, channelID, topicID, messageID kernel.ID) error {
	return r.change(ctx, m, channelID, topicID, &messageID)
}

// Reopen clears a topic's conclusion; an open topic is unchanged.
func (r *Resolution) Reopen(ctx context.Context, m org.Membership, channelID, topicID kernel.ID) error {
	return r.change(ctx, m, channelID, topicID, nil)
}

func (r *Resolution) change(ctx context.Context, m org.Membership, channelID, topicID kernel.ID, messageID *kernel.ID) error {
	organizationID := m.Organization.ID
	if organizationID == (kernel.ID{}) || m.Member.ID == (kernel.ID{}) || m.Member.OrganizationID != organizationID {
		return org.ErrNotFound
	}
	// As with pinning, rollback releases the reserved sequence on a no-op.
	unchanged := errors.New("resolution unchanged")
	var seq int64
	err := r.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		seq, err = r.sequences(tx).NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		w := r.writer(tx)
		// Every resolved member may write every public channel, as for pinning.
		topic, err := w.GetTopic(ctx, organizationID, channelID, topicID)
		if err != nil {
			return err
		}
		var conclusion kernel.ID
		if messageID != nil {
			if topic.IsDefault {
				return ErrDefaultTopicResolution
			}
			// This existing scoped message lookup also serves quoted messages.
			message, err := w.QuotedMessage(ctx, organizationID, channelID, *messageID)
			if errors.Is(err, ErrQuoteNotFound) {
				return ErrMessageNotFound
			}
			if err != nil {
				return err
			}
			if message.TopicID != topicID {
				return ErrConclusionConflict
			}
			conclusion = message.ID
		}
		if conclusion == topic.ConclusionMessageID {
			return unchanged
		}
		if err := w.SetTopicConclusion(ctx, organizationID, channelID, topicID, conclusion); err != nil {
			return err
		}
		return r.events(tx).Append(ctx, organizationID, seq, KindResolutionChanged, nil, EncodeResolutionChanged(channelID, topicID))
	})
	if errors.Is(err, unchanged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("changing topic resolution: %w", err)
	}
	if r.notifier != nil {
		r.notifier.Raise(organizationID, seq)
	}
	return nil
}
