package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrPinLimit means a new pin would exceed MaxChannelPins.
var ErrPinLimit = errors.New("channel pin limit reached")

// PinWriterIn binds pin metadata to the caller's transaction.
type PinWriterIn func(platform.Tx) PinWriter

// Pinning commits pin changes with their durable invalidation, without read writes.
type Pinning struct {
	runner    TxRunner
	writer    WriterIn
	pins      PinWriterIn
	sequences EventSequenceIn
	events    EventAppenderIn
	notifier  Notifier
}

// NewPinning builds pinning over transaction-bound factories; notifier may be nil.
func NewPinning(runner TxRunner, writer WriterIn, pins PinWriterIn, sequences EventSequenceIn, events EventAppenderIn, notifier Notifier) *Pinning {
	return &Pinning{runner, writer, pins, sequences, events, notifier}
}

// Pin pins any message the resolved member may write in the channel.
func (p *Pinning) Pin(ctx context.Context, m org.Membership, channelID, messageID kernel.ID) error {
	return p.change(ctx, m, channelID, messageID, true)
}

// Unpin removes any member's pin in a channel the resolved member may write.
func (p *Pinning) Unpin(ctx context.Context, m org.Membership, channelID, messageID kernel.ID) error {
	return p.change(ctx, m, channelID, messageID, false)
}

func (p *Pinning) change(ctx context.Context, m org.Membership, channelID, messageID kernel.ID, pin bool) error {
	organizationID := m.Organization.ID
	if organizationID == (kernel.ID{}) || m.Member.ID == (kernel.ID{}) || m.Member.OrganizationID != organizationID {
		return org.ErrNotFound
	}
	// Rolling back also releases the reserved sequence on an idempotent request.
	unchanged := errors.New("pin unchanged")
	var seq int64
	err := p.runner.InTx(ctx, func(tx platform.Tx) error {
		var err error
		seq, err = p.sequences(tx).NextEventSeq(ctx, organizationID)
		if err != nil {
			return err
		}
		// Public channels currently grant every resolved member write access,
		// exactly as posting does. Keep this check before reading pin metadata.
		if _, err := p.writer(tx).GetDefaultTopic(ctx, organizationID, channelID); err != nil {
			if errors.Is(err, ErrTopicNotFound) {
				return ErrChannelNotFound
			}
			return err
		}
		w := p.pins(tx)
		state, err := w.GetMessagePin(ctx, organizationID, channelID, messageID)
		if err != nil {
			return err
		}
		if pin == !state.PinnedAt.IsZero() {
			return unchanged
		}
		if pin {
			count, countErr := w.CountChannelPins(ctx, organizationID, channelID)
			if countErr != nil {
				return countErr
			}
			if count >= MaxChannelPins {
				return ErrPinLimit
			}
			err = w.SetMessagePin(ctx, organizationID, channelID, messageID, m.Member.ID)
		} else {
			err = w.ClearMessagePin(ctx, organizationID, channelID, messageID)
		}
		if err != nil {
			return err
		}
		return p.events(tx).Append(ctx, organizationID, seq, KindPinsChanged, nil, EncodePinsChanged(channelID, messageID))
	})
	if errors.Is(err, unchanged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("changing pin: %w", err)
	}
	if p.notifier != nil {
		p.notifier.Raise(organizationID, seq)
	}
	return nil
}
