package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ChannelStore persists channels using a pool or a caller-owned transaction.
type ChannelStore struct{ queries *sqlcgen.Queries }

// NewChannelStore returns a store using db.
func NewChannelStore(db sqlcgen.DBTX) *ChannelStore {
	return &ChannelStore{queries: sqlcgen.New(db)}
}

// CreateChannel inserts a non-default channel with an already validated name.
func (s *ChannelStore) CreateChannel(ctx context.Context, organizationID kernel.ID, name string) (conversation.Channel, error) {
	return s.createChannel(ctx, organizationID, name, false)
}

func (s *ChannelStore) createChannel(ctx context.Context, organizationID kernel.ID, name string, isDefault bool) (conversation.Channel, error) {
	row, err := s.queries.CreateChannel(ctx, sqlcgen.CreateChannelParams{OrganizationID: uuid(organizationID), Name: name, IsDefault: isDefault})
	var pgErr *pgconn.PgError
	switch {
	// The unique constraint, not a lookup first, decides between concurrent creators.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_organization_id_name_key":
		return conversation.Channel{}, conversation.ErrChannelNameTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "channel_name_"):
		return conversation.Channel{}, fmt.Errorf("%w: %w", conversation.ErrInvalidChannelName, err)
	case err != nil:
		return conversation.Channel{}, fmt.Errorf("inserting channel: %w", err)
	}
	return channelFromRow(sqlcgen.Channel(row)), nil
}

// ListChannels returns the organisation's channels ordered by name and ID.
func (s *ChannelStore) ListChannels(ctx context.Context, organizationID kernel.ID) ([]conversation.Channel, error) {
	rows, err := s.queries.ListChannels(ctx, uuid(organizationID))
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	channels := make([]conversation.Channel, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, channelFromRow(row))
	}
	return channels, nil
}

// GetChannel looks up an ID within the organisation.
func (s *ChannelStore) GetChannel(ctx context.Context, organizationID, id kernel.ID) (conversation.Channel, error) {
	row, err := s.queries.GetChannel(ctx, sqlcgen.GetChannelParams{OrganizationID: uuid(organizationID), ID: uuid(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Channel{}, conversation.ErrChannelNotFound
	}
	if err != nil {
		return conversation.Channel{}, fmt.Errorf("getting channel: %w", err)
	}
	return channelFromRow(row), nil
}

// GetDefaultChannel returns the organisation's default, if one exists.
func (s *ChannelStore) GetDefaultChannel(ctx context.Context, organizationID kernel.ID) (conversation.Channel, error) {
	row, err := s.queries.GetDefaultChannel(ctx, uuid(organizationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversation.Channel{}, conversation.ErrChannelNotFound
	}
	if err != nil {
		return conversation.Channel{}, fmt.Errorf("getting default channel: %w", err)
	}
	return channelFromRow(row), nil
}

func channelFromRow(row sqlcgen.Channel) conversation.Channel {
	return conversation.Channel{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, DefaultTopicID: row.DefaultTopicID.Bytes, Name: row.Name, IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time}
}
