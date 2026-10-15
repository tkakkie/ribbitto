package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

var _ conversation.PinWriter = Writer{}
var _ conversation.PinReader = ReadStore{}

// GetMessagePin distinguishes an unpinned message from a missing scoped message.
func (w Writer) GetMessagePin(ctx context.Context, organizationID, channelID, messageID kernel.ID) (conversation.PinState, error) {
	row, err := w.queries.GetMessagePin(ctx, sqlcgen.GetMessagePinParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), ID: uuid(messageID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.PinState{}, conversation.ErrMessageNotFound
	}
	if err != nil {
		return conversation.PinState{}, fmt.Errorf("getting message pin: %w", err)
	}
	return conversation.PinState{PinnedByMemberID: row.PinnedByMemberID.Bytes, PinnedAt: row.PinnedAt.Time}, nil
}

// CountChannelPins counts pins across all topics of the scoped channel.
func (w Writer) CountChannelPins(ctx context.Context, organizationID, channelID kernel.ID) (int64, error) {
	count, err := w.queries.CountChannelPins(ctx, sqlcgen.CountChannelPinsParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID)})
	if err != nil {
		return 0, fmt.Errorf("counting channel pins: %w", err)
	}
	return count, nil
}

// SetMessagePin records the pinner and server time after the caller's checks.
func (w Writer) SetMessagePin(ctx context.Context, organizationID, channelID, messageID, pinnerID kernel.ID) error {
	n, err := w.queries.SetMessagePin(ctx, sqlcgen.SetMessagePinParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), ID: uuid(messageID), PinnedByMemberID: uuid(pinnerID), PinnedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	return pinWriteResult(n, err)
}

// ClearMessagePin removes only metadata, preserving the current message.
func (w Writer) ClearMessagePin(ctx context.Context, organizationID, channelID, messageID kernel.ID) error {
	n, err := w.queries.ClearMessagePin(ctx, sqlcgen.ClearMessagePinParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), ID: uuid(messageID)})
	return pinWriteResult(n, err)
}

func pinWriteResult(n int64, err error) error {
	if err != nil {
		return fmt.Errorf("writing message pin: %w", err)
	}
	if n == 0 {
		return conversation.ErrMessageNotFound
	}
	return nil
}

// ListChannelPins bounds current content without copying it into pin storage.
func (s ReadStore) ListChannelPins(ctx context.Context, organizationID, channelID kernel.ID) ([]conversation.PinnedMessage, error) {
	rows, err := s.queries.ListChannelPins(ctx, sqlcgen.ListChannelPinsParams{OrganizationID: uuid(organizationID), ChannelID: uuid(channelID), Limit: conversation.MaxChannelPins})
	if err != nil {
		return nil, fmt.Errorf("listing channel pins: %w", err)
	}
	pins := make([]conversation.PinnedMessage, 0, len(rows))
	for _, row := range rows {
		pins = append(pins, conversation.PinnedMessage{Message: messageFromRow(row), PinState: conversation.PinState{PinnedByMemberID: row.PinnedByMemberID.Bytes, PinnedAt: row.PinnedAt.Time}})
	}
	return pins, nil
}
