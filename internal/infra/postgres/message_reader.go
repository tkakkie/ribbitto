package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/sqlcgen"
)

// MessageReader reads the channel page and its cursor, or one message, each
// with both author batches and topic labels from one snapshot.
type MessageReader struct{ Pool *pgxpool.Pool }

// One reads one message, its author names and topic in a read-only snapshot.
func (s MessageReader) One(ctx context.Context, m authz.Membership, channelID domain.ID, eventSeq int64) (entry message.Entry, err error) {
	err = pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		reader := message.Reader{History: NewMessageStore(tx), Members: NewMemberStore(tx), Accounts: NewAccountStore(tx), Topics: NewTopicStore(tx)}
		entry, err = reader.One(ctx, m, channelID, eventSeq)
		return err
	})
	if err != nil {
		return message.Entry{}, fmt.Errorf("reading message snapshot: %w", err)
	}
	return entry, nil
}

// Before reads a page for a resolved member, omitting the cursor on older pages.
func (s MessageReader) Before(ctx context.Context, m authz.Membership, channelID domain.ID, before *int64) (message.ChannelPage, error) {
	return s.Page(ctx, m, channelID, nil, before)
}

// Page reads channel or topic history; the latest page of either carries the
// snapshot's event cursor for its stream, older pages none.
func (s MessageReader) Page(ctx context.Context, m authz.Membership, channelID domain.ID, topicID *domain.ID, before *int64) (page message.ChannelPage, err error) {
	err = pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		channels := channel.New(NewChannelStore(tx))
		page.Current, err = channels.Get(ctx, m, channelID)
		if err != nil {
			return err
		}
		page.Channels, err = channels.List(ctx, m)
		if err != nil {
			return err
		}
		var topics topic.Store = NewTopicStore(tx)
		if topicID != nil {
			selected, err := topics.GetTopic(ctx, m.Organization.ID, channelID, *topicID)
			if err != nil {
				return err
			}
			page.Topic = &selected
		}
		page.Topics, err = topics.ListTopics(ctx, m.Organization.ID, channelID, 50)
		if err != nil {
			return err
		}
		reader := message.Reader{History: NewMessageStore(tx), Members: NewMemberStore(tx), Accounts: NewAccountStore(tx), Topics: NewTopicStore(tx)}
		page.Page, err = reader.Before(ctx, m, channelID, topicID, before)
		if err != nil {
			return err
		}
		if before == nil {
			seq, err := sqlcgen.New(tx).GetEventSeq(ctx, pgtype.UUID{Bytes: m.Organization.ID, Valid: true})
			if err != nil {
				return fmt.Errorf("reading page cursor: %w", err)
			}
			page.EventCursor = &seq
		}
		return nil
	})
	if err != nil {
		return message.ChannelPage{}, fmt.Errorf("reading channel page snapshot: %w", err)
	}
	return page, nil
}
