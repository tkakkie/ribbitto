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
	runner      SnapshotRunner
	countRunner SnapshotRunner
	reads       ReadStoreIn
	members     MemberDirectoryIn
	accounts    AccountDirectoryIn
	cursor      EventCursorIn
	counts      PageCountsIn
}

// NewReader builds the snapshot use case with conversation's reads and the
// member, account, cursor and count factories supplied by the composition root.
// countRunner is used only for page and sidebar counts. Nil counts omits that
// work and uses runner for all snapshots, such as seed's expected-history export.
func NewReader(runner, countRunner SnapshotRunner, reads ReadStoreIn, members MemberDirectoryIn, accounts AccountDirectoryIn, cursor EventCursorIn, counts PageCountsIn) *Reader {
	if counts == nil {
		countRunner = runner
	}
	return &Reader{runner: runner, countRunner: countRunner, reads: reads, members: members, accounts: accounts, cursor: cursor, counts: counts}
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
func (s *Reader) Page(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, before *int64) (ChannelPage, error) {
	return s.page(ctx, m, channelID, topicID, func(reader historyReader) (Page, error) {
		return reader.Before(ctx, m, channelID, topicID, before)
	}, false)
}

// Around reads the older page holding event_seq around, with context on
// both sides, in the same snapshot as Page.
func (s *Reader) Around(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, around int64) (ChannelPage, error) {
	return s.page(ctx, m, channelID, topicID, func(reader historyReader) (Page, error) {
		return reader.Around(ctx, m, channelID, topicID, around)
	}, false)
}

func (s *Reader) page(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID, read func(historyReader) (Page, error), withPins bool) (page ChannelPage, err error) {
	err = s.countRunner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		loaded, err := loadSidebarContext(ctx, reads, m.Organization.ID, channelID, topicID)
		if err != nil {
			return err
		}
		page.Current, page.Channels, page.Topics = loaded.current, loaded.channels, loaded.topics
		page.Topic = loaded.topic
		page.Page, err = read(s.history(snapshot, reads))
		if err != nil {
			return err
		}
		if withPins {
			if page.Pins, err = s.pins(ctx, snapshot, reads, m, channelID); err != nil {
				return err
			}
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

// Locate finds where a message of the member's organisation is now, for its
// URL (decision 36). Every channel is public, so the organisation scope is
// the whole read check; an unknown ID and another organisation's message are
// both ErrMessageNotFound, so the answer never says which.
func (s *Reader) Locate(ctx context.Context, m org.Membership, id kernel.ID) (at Location, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		var newer int
		at, newer, err = s.reads(snapshot).LocateMessage(ctx, m.Organization.ID, id, PageSize)
		at.Latest = newer < PageSize
		return err
	})
	if err != nil {
		return Location{}, fmt.Errorf("locating message: %w", err)
	}
	return at, nil
}

// Many reads a bounded batch of the channel's messages, such as a move's or
// one to quote, with their quotes and directories in one snapshot. An ID
// missing or outside the organisation and channel is ErrMessageNotFound.
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
