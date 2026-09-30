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

// MessageReader reads messages and both author batches from the same snapshot.
type MessageReader struct{ Pool *pgxpool.Pool }

// One reads one message and its author names in a read-only snapshot.
func (s MessageReader) One(ctx context.Context, m authz.Membership, channelID domain.ID, eventSeq int64) (entry message.Entry, err error) {
	err = pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		reader := message.Reader{History: NewMessageStore(tx), Members: NewMemberStore(tx), Accounts: NewAccountStore(tx)}
		entry, err = reader.One(ctx, m, channelID, eventSeq)
		return err
	})
	if err != nil {
		return message.Entry{}, fmt.Errorf("reading message snapshot: %w", err)
	}
	return entry, nil
}

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
