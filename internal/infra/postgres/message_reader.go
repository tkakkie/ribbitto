package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
)

// MessageReader reads history and both author batches from the same snapshot.
type MessageReader struct{ Pool *pgxpool.Pool }

// Latest implements the channel page's message read in a read-only snapshot.
func (s MessageReader) Latest(ctx context.Context, m authz.Membership, channelID domain.ID) (entries []message.Entry, err error) {
	err = pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		reader := message.Reader{History: NewMessageStore(tx), Members: NewMemberStore(tx), Accounts: NewAccountStore(tx)}
		entries, err = reader.Latest(ctx, m, channelID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading message snapshot: %w", err)
	}
	return entries, nil
}
