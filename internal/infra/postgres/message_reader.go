package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// MessageReader reads the channel page and its cursor, or one message, each
// with both author batches and topic labels from one snapshot.
type MessageReader struct {
	Pool *pgxpool.Pool
	// Accounts returns identity's display-name directory bound to the
	// snapshot the reader opened (identitypg.AccountsIn). Temporary until
	// migration step 4, when conversation owns the page snapshot.
	Accounts func(platform.Snapshot) identity.Directory
	// Members returns org's member directory bound to the same snapshot
	// (orgpg.MembersIn, adapted by a closure). Temporary until step 4 too.
	Members MemberDirectoryIn
	// Cursor returns org's committed event_seq bound to the same snapshot
	// (orgpg.EventCursorIn, adapted by a closure), for the latest page.
	// Temporary until step 4 too.
	Cursor EventCursorIn
}

// One reads one message, its author names and topic in a read-only snapshot.
func (s MessageReader) One(ctx context.Context, m org.Membership, channelID domain.ID, eventSeq int64) (entry message.Entry, err error) {
	err = platform.InSnapshot(ctx, s.Pool, func(snapshot platform.Snapshot) error {
		tx := pgxbridge.Snapshot(snapshot)
		reader := message.Reader{History: NewMessageStore(tx), Members: s.Members(snapshot), Accounts: s.Accounts(snapshot), Topics: NewTopicStore(tx)}
		entry, err = reader.One(ctx, m, channelID, eventSeq)
		return err
	})
	if err != nil {
		return message.Entry{}, fmt.Errorf("reading message snapshot: %w", err)
	}
	return entry, nil
}

// Before reads a page for a resolved member, omitting the cursor on older pages.
func (s MessageReader) Before(ctx context.Context, m org.Membership, channelID domain.ID, before *int64) (message.ChannelPage, error) {
	return s.Page(ctx, m, channelID, nil, before)
}

// Page reads channel or topic history; the latest page of either carries the
// snapshot's event cursor for its stream, older pages none.
func (s MessageReader) Page(ctx context.Context, m org.Membership, channelID domain.ID, topicID *domain.ID, before *int64) (page message.ChannelPage, err error) {
	err = platform.InSnapshot(ctx, s.Pool, func(snapshot platform.Snapshot) error {
		tx := pgxbridge.Snapshot(snapshot)
		channels := conversation.NewChannels(NewChannelStore(tx))
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
		reader := message.Reader{History: NewMessageStore(tx), Members: s.Members(snapshot), Accounts: s.Accounts(snapshot), Topics: NewTopicStore(tx)}
		page.Page, err = reader.Before(ctx, m, channelID, topicID, before)
		if err != nil {
			return err
		}
		if before == nil {
			seq, err := s.Cursor(snapshot).EventSeq(ctx, m.Organization.ID)
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

// Many reads a move's bounded message batch and directories in one snapshot.
func (s MessageReader) Many(ctx context.Context, m org.Membership, channelID domain.ID, ids []domain.ID) (entries []message.Entry, err error) {
	err = platform.InSnapshot(ctx, s.Pool, func(snapshot platform.Snapshot) error {
		tx := pgxbridge.Snapshot(snapshot)
		reader := message.Reader{History: NewMessageStore(tx), Members: s.Members(snapshot), Accounts: s.Accounts(snapshot), Topics: NewTopicStore(tx)}
		entries, err = reader.Many(ctx, m, channelID, ids)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading messages snapshot: %w", err)
	}
	return entries, nil
}
