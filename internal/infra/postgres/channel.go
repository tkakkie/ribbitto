package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// ChannelStore persists channels using a pool or a caller-owned transaction.
type ChannelStore struct{ queries *sqlcgen.Queries }

// NewChannelStore returns a store using db.
func NewChannelStore(db sqlcgen.DBTX) *ChannelStore {
	return &ChannelStore{queries: sqlcgen.New(db)}
}

// CreateChannel inserts a channel with an already validated name.
func (s *ChannelStore) CreateChannel(ctx context.Context, organizationID domain.ID, name string, isDefault bool) (domain.Channel, error) {
	row, err := s.queries.CreateChannel(ctx, sqlcgen.CreateChannelParams{OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, Name: name, IsDefault: isDefault})
	var pgErr *pgconn.PgError
	switch {
	// The unique constraint, not a lookup first, decides between concurrent creators.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_organization_id_name_key":
		return domain.Channel{}, channel.ErrNameTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.HasPrefix(pgErr.ConstraintName, "channel_name_"):
		return domain.Channel{}, fmt.Errorf("%w: %w", channel.ErrInvalidName, err)
	case err != nil:
		return domain.Channel{}, fmt.Errorf("creating channel: %w", err)
	}
	return channelFromRow(sqlcgen.Channel(row)), nil
}

// ListChannels returns the organisation's channels ordered by name and ID.
func (s *ChannelStore) ListChannels(ctx context.Context, organizationID domain.ID) ([]domain.Channel, error) {
	rows, err := s.queries.ListChannels(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("listing channels: %w", err)
	}
	channels := make([]domain.Channel, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, channelFromRow(row))
	}
	return channels, nil
}

// GetChannel looks up an ID within the organisation.
func (s *ChannelStore) GetChannel(ctx context.Context, organizationID, id domain.ID) (domain.Channel, error) {
	row, err := s.queries.GetChannel(ctx, sqlcgen.GetChannelParams{OrganizationID: pgtype.UUID{Bytes: organizationID, Valid: true}, ID: pgtype.UUID{Bytes: id, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, channel.ErrNotFound
	}
	if err != nil {
		return domain.Channel{}, fmt.Errorf("getting channel: %w", err)
	}
	return channelFromRow(row), nil
}

// GetDefaultChannel returns the organisation's default, if one exists.
func (s *ChannelStore) GetDefaultChannel(ctx context.Context, organizationID domain.ID) (domain.Channel, error) {
	row, err := s.queries.GetDefaultChannel(ctx, pgtype.UUID{Bytes: organizationID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, channel.ErrNotFound
	}
	if err != nil {
		return domain.Channel{}, fmt.Errorf("getting default channel: %w", err)
	}
	return channelFromRow(row), nil
}

func channelFromRow(row sqlcgen.Channel) domain.Channel {
	return domain.Channel{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, DefaultTopicID: row.DefaultTopicID.Bytes, Name: row.Name, IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time}
}
