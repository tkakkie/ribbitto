package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Reader owns page, sidebar, single-message and batch snapshots across conversation,
// org, identity and unread through factories bound to the same read-only snapshot.
type Reader struct {
	runner   SnapshotRunner
	reads    ReadStoreIn
	members  MemberDirectoryIn
	accounts AccountDirectoryIn
	cursor   EventCursorIn
	counts   PageCountsIn
	witness  WitnessIn
}

// NewReader builds the snapshot use case with conversation's reads and the
// member, account, cursor and count factories supplied by the composition root.
func NewReader(runner SnapshotRunner, reads ReadStoreIn, members MemberDirectoryIn, accounts AccountDirectoryIn, cursor EventCursorIn, counts PageCountsIn) *Reader {
	return &Reader{runner: runner, reads: reads, members: members, accounts: accounts, cursor: cursor, counts: counts}
}

func (s *Reader) history(snapshot platform.Snapshot, reads ReadStore) historyReader {
	return historyReader{Reads: reads, Members: s.members(snapshot), Accounts: s.accounts(snapshot)}
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

// Page reads channel or topic history with the snapshot's event cursor,
// including older pages with a before bound.
func (s *Reader) Page(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, before *int64) (page ChannelPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		loaded, err := loadSidebarContext(ctx, reads, m.Organization.ID, channelID, topicID)
		if err != nil {
			return err
		}
		page.Current, page.Channels, page.Topics = loaded.current, loaded.channels, loaded.topics
		page.Topic = loaded.topic
		reader := s.history(snapshot, reads)
		page.Page, err = reader.Before(ctx, m, channelID, topicID, before)
		if err != nil {
			return err
		}
		seq, err := s.cursor(snapshot).EventSeq(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("reading page cursor: %w", err)
		}
		page.EventCursor = &seq
		page.PageCounts, err = s.pageCounts(ctx, snapshot, m, channelID, page.Channels, page.Topics, topicID)
		return err
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
