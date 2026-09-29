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

// Before implements the channel page's history read in a read-only snapshot.
func (s MessageReader) Before(ctx context.Context, m authz.Membership, channelID domain.ID, before *int64) (page message.Page, err error) {
	err = pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		reader := message.Reader{History: NewMessageStore(tx), Members: NewMemberStore(tx), Accounts: NewAccountStore(tx)}
		page, err = reader.Before(ctx, m, channelID, before)
		return err
	})
	if err != nil {
		return message.Page{}, fmt.Errorf("reading message snapshot: %w", err)
	}
	return page, nil
}
