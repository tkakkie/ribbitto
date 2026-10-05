package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Reader owns the page, single-message and batch snapshots across conversation,
// org and identity through factories bound to the same read-only snapshot.
type Reader struct {
	runner   SnapshotRunner
	reads    ReadStoreIn
	members  MemberDirectoryIn
	accounts AccountDirectoryIn
	cursor   EventCursorIn
}

// NewReader builds the snapshot use case with conversation's reads and the
// member, account and cursor factories supplied by the composition root.
func NewReader(runner SnapshotRunner, reads ReadStoreIn, members MemberDirectoryIn, accounts AccountDirectoryIn, cursor EventCursorIn) *Reader {
	return &Reader{runner: runner, reads: reads, members: members, accounts: accounts, cursor: cursor}
}

func (s *Reader) history(snapshot platform.Snapshot, reads ReadStore) historyReader {
	return historyReader{History: reads, Members: s.members(snapshot), Accounts: s.accounts(snapshot), Topics: reads}
}

// One reads one message, its author names and topic in a read-only snapshot.
func (s *Reader) One(ctx context.Context, m org.Membership, channelID kernel.ID, eventSeq int64) (entry Entry, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reader := s.history(snapshot, s.reads(snapshot))
		entry, err = reader.One(ctx, m, channelID, eventSeq)
		return err
	})
	if err != nil {
		return Entry{}, fmt.Errorf("reading message snapshot: %w", err)
	}
	return entry, nil
}

// Page reads channel or topic history; the latest page of either carries the
// snapshot's event cursor for its stream, older pages none.
func (s *Reader) Page(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, before *int64) (page ChannelPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		page.Current, err = reads.GetChannel(ctx, m.Organization.ID, channelID)
		if err != nil {
			return fmt.Errorf("finding channel: %w", err)
		}
		page.Channels, err = reads.ListChannels(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("listing channels: %w", err)
		}
		if topicID != nil {
			selected, err := reads.GetTopic(ctx, m.Organization.ID, channelID, *topicID)
			if err != nil {
				return err
			}
			page.Topic = &selected
		}
		page.Topics, err = reads.ListTopics(ctx, m.Organization.ID, channelID, 50)
		if err != nil {
			return err
		}
		reader := s.history(snapshot, reads)
		page.Page, err = reader.Before(ctx, m, channelID, topicID, before)
		if err != nil {
			return err
		}
		if before == nil {
			seq, err := s.cursor(snapshot).EventSeq(ctx, m.Organization.ID)
			if err != nil {
				return fmt.Errorf("reading page cursor: %w", err)
			}
			page.EventCursor = &seq
		}
		return nil
	})
	if err != nil {
		return ChannelPage{}, fmt.Errorf("reading channel page snapshot: %w", err)
	}
	return page, nil
}

// Many reads a move's bounded message batch and directories in one snapshot.
func (s *Reader) Many(ctx context.Context, m org.Membership, channelID kernel.ID, ids []kernel.ID) (entries []Entry, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reader := s.history(snapshot, s.reads(snapshot))
		entries, err = reader.Many(ctx, m, channelID, ids)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading messages snapshot: %w", err)
	}
	return entries, nil
}
