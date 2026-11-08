package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ReplyReader reads at most 51 replies, newest first, applying both the
// readable-channel set and author exclusion before the limit in one statement.
type ReplyReader interface {
	ListReplies(context.Context, kernel.ID, kernel.ID, []kernel.ID, *int64) ([]Message, error)
}

// ReplyReaderIn binds the reply source to a caller's snapshot.
type ReplyReaderIn func(platform.Snapshot) ReplyReader

// ReplyPage contains up to 50 messages and the exclusive event_seq cursor
// for the next page, or nil when no older matching message exists.
type ReplyPage struct {
	Messages []Message
	Next     *int64
}

// Replies reads replies to one member's messages across readable channels.
type Replies struct {
	runner   SnapshotRunner
	reads    ReplyReaderIn
	channels ReadableChannelsIn
}

// NewReplies shares a snapshot between the read-access port and page source.
func NewReplies(runner SnapshotRunner, reads ReplyReaderIn, channels ReadableChannelsIn) *Replies {
	return &Replies{runner: runner, reads: reads, channels: channels}
}

// Page reads replies to the resolved member's messages, excluding their own messages.
// A cursor grants no access: organisation, member and channel filters still apply.
func (s *Replies) Page(ctx context.Context, m org.Membership, before *int64) (page ReplyPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		ids, err := s.channels(snapshot).ReadableChannelIDs(ctx, m)
		if err != nil {
			return fmt.Errorf("reading readable channels: %w", err)
		}
		messages, err := s.reads(snapshot).ListReplies(ctx, m.Organization.ID, m.Member.ID, ids, before)
		if err != nil {
			return err
		}
		if len(messages) > PageSize {
			messages = messages[:PageSize]
			page.Next = &messages[len(messages)-1].EventSeq
		}
		page.Messages = messages
		return nil
	})
	if err != nil {
		return ReplyPage{}, fmt.Errorf("reading reply page: %w", err)
	}
	return page, nil
}
