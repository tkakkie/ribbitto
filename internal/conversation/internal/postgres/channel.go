package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// ChannelStore persists channels using a pool or a caller-owned transaction.
type ChannelStore struct {
	queries     *sqlcgen.Queries
	maxChannels int32
}

// NewChannelStore returns a store using db.
func NewChannelStore(db sqlcgen.DBTX, maxChannels int32) *ChannelStore {
	return &ChannelStore{queries: sqlcgen.New(db), maxChannels: maxChannels}
}

// ChannelCreatorIn binds capped channel creation to the caller's transaction.
func ChannelCreatorIn(tx platform.Tx, maxChannels int32) *ChannelStore {
	return NewChannelStore(pgxbridge.Tx(tx), maxChannels)
}

// LockCreation must precede creation in the caller's READ COMMITTED transaction.
func (s *ChannelStore) LockCreation(ctx context.Context, organizationID kernel.ID) error {
	if _, err := s.queries.LockChannelCreation(ctx, uuid(organizationID)); err != nil {
		return fmt.Errorf("locking channel creation: %w", err)
	}
	return nil
}

// CreateChannel inserts a non-default channel with an already validated name.
func (s *ChannelStore) CreateChannel(ctx context.Context, organizationID kernel.ID, name, image string) (conversation.Channel, error) {
	return s.createChannel(ctx, organizationID, name, false, image)
}

func (s *ChannelStore) createChannel(ctx context.Context, organizationID kernel.ID, name string, isDefault bool, image string) (conversation.Channel, error) {
	row, err := s.queries.CreateChannel(ctx, sqlcgen.CreateChannelParams{OrganizationID: uuid(organizationID), Name: name, IsDefault: isDefault, MaxChannels: int64(s.maxChannels), Image: pgtype.Text{String: image, Valid: image != ""}})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return conversation.Channel{}, conversation.ErrChannelLimit
	// The unique index, not a lookup first, decides between concurrent creators.
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_name_idx":
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
		return nil, fmt.Errorf("selecting channels: %w", err)
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
	return conversation.Channel{ID: row.ID.Bytes, OrganizationID: row.OrganizationID.Bytes, DefaultTopicID: row.DefaultTopicID.Bytes, Name: row.Name, Image: row.Image.String, IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time}
}

// SetChannelImage stores a key, or NULL for the ID-based default, in one scoped write.
func (s *ChannelStore) SetChannelImage(ctx context.Context, organizationID, id kernel.ID, image string) error {
	rows, err := s.queries.SetChannelImage(ctx, sqlcgen.SetChannelImageParams{OrganizationID: uuid(organizationID), ID: uuid(id), Image: pgtype.Text{String: image, Valid: image != ""}})
	if err != nil {
		return fmt.Errorf("updating channel image: %w", err)
	}
	if rows == 0 {
		return conversation.ErrChannelNotFound
	}
	return nil
}
