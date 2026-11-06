package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ReadableChannels supplies the channels a resolved member may read.
// The current public-channel implementation returns all organisation channels.
type ReadableChannels interface {
	ReadableChannelIDs(context.Context, org.Membership) ([]kernel.ID, error)
}

// ReadableChannelsIn binds the shared read-access port to a caller's snapshot.
type ReadableChannelsIn func(platform.Snapshot) ReadableChannels

// MentionReader reads at most 51 mentions, newest first, applying both the
// readable-channel set and author exclusion before the limit in one statement.
type MentionReader interface {
	ListMentions(context.Context, kernel.ID, kernel.ID, []kernel.ID, *int64) ([]Message, error)
}

// MentionReaderIn binds the mention source to a caller's snapshot.
type MentionReaderIn func(platform.Snapshot) MentionReader

// MentionPage contains up to 50 messages and the exclusive event_seq cursor
// for the next page, or nil when no older matching message exists.
type MentionPage struct {
	Messages []Message
	Next     *int64
}

// Mentions reads one member's mentions across readable channels.
type Mentions struct {
	runner   SnapshotRunner
	reads    MentionReaderIn
	channels ReadableChannelsIn
}

// NewMentions shares a snapshot between the read-access port and page source.
func NewMentions(runner SnapshotRunner, reads MentionReaderIn, channels ReadableChannelsIn) *Mentions {
	return &Mentions{runner: runner, reads: reads, channels: channels}
}

// Page reads the resolved member's mentions, excluding their own messages.
// A cursor grants no access: organisation, member and channel filters still apply.
func (s *Mentions) Page(ctx context.Context, m org.Membership, before *int64) (page MentionPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		ids, err := s.channels(snapshot).ReadableChannelIDs(ctx, m)
		if err != nil {
			return fmt.Errorf("reading readable channels: %w", err)
		}
		messages, err := s.reads(snapshot).ListMentions(ctx, m.Organization.ID, m.Member.ID, ids, before)
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
		return MentionPage{}, fmt.Errorf("reading mention page: %w", err)
	}
	return page, nil
}
