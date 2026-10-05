package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// MemberDirectory is the author lookup the page reader needs from org. It
// returns only the requested members of one organisation, omitting missing
// and foreign ones, as org's result type (no copy of it here).
type MemberDirectory interface {
	LookupMembers(ctx context.Context, organizationID kernel.ID, memberIDs []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error)
}

// MemberDirectoryIn binds a member directory to the caller's snapshot, so the
// authors are read in the same snapshot as the messages they wrote.
type MemberDirectoryIn func(platform.Snapshot) MemberDirectory

// AccountDirectory is the display-name lookup the page reader needs from
// identity. It returns only the requested accounts, omitting missing ones.
type AccountDirectory interface {
	LookupDisplayNames(ctx context.Context, accountIDs []kernel.ID) (map[kernel.ID]string, error)
}

// AccountDirectoryIn binds an account directory to the caller's snapshot, the
// same one the member directory and the messages are read in.
type AccountDirectoryIn func(platform.Snapshot) AccountDirectory

// TxRunner runs posting's and branching's transaction (decision 26): the use
// case owns the boundary, the writers only bind to the Tx they are given.
type TxRunner interface {
	// InTx runs fn in a new transaction. It commits when fn returns nil
	// and rolls back otherwise, returning fn's error as is. A failed commit,
	// such as event_log's deferred gap check, is returned too.
	InTx(ctx context.Context, fn func(platform.Tx) error) error
}

// Writer is posting's and branching's reads and writes in the caller's
// transaction, on validated values. It takes no organisation lock (the
// sequence, taken first, does) and never begins, commits or rolls back: the
// caller does, also after an error, which leaves the transaction unusable.
type Writer interface {
	// GetDefaultTopic returns the channel's default topic; none, as for
	// another organisation's channel, is ErrTopicNotFound.
	GetDefaultTopic(ctx context.Context, organizationID, channelID kernel.ID) (Topic, error)
	// GetTopic returns a topic of the channel, or ErrTopicNotFound.
	GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (Topic, error)
	// InsertMessage inserts posting's message at the sequence the caller
	// took, into a topic it resolved in the same transaction first
	// (GetDefaultTopic or GetTopic). Another organisation's channel is
	// ErrChannelNotFound and another organisation's member org.ErrNotFound.
	InsertMessage(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, eventSeq int64) (Message, error)
	// CreateTopic creates a named topic. A name the channel already has is
	// ErrTopicNameTaken; one the database rejects is ErrInvalidTopicName.
	CreateTopic(ctx context.Context, organizationID, channelID kernel.ID, name string) (Topic, error)
	// InsertNotice inserts branching's notice like InsertMessage but maps no
	// error, so a failure there stays a server error (R2 on #502).
	InsertNotice(ctx context.Context, organizationID, channelID, topicID, memberID kernel.ID, body string, eventSeq int64) (Message, error)
	// MoveMessages moves the given messages still in the source topic of
	// the channel to the destination and returns how many moved.
	MoveMessages(ctx context.Context, organizationID, channelID, fromTopicID, toTopicID kernel.ID, messageIDs []kernel.ID) (int64, error)
}

// WriterIn binds conversation's writes to the caller's transaction.
type WriterIn func(platform.Tx) Writer

// EventSequence is what posting and branching need from org in their
// transaction: the organisation's next event_seq, taken first, whose row
// lock makes sequence order commit order (decision 5). An unknown
// organisation is org.ErrNotFound.
type EventSequence interface {
	NextEventSeq(ctx context.Context, organizationID kernel.ID) (int64, error)
}

// EventSequenceIn binds org's sequence to the caller's transaction.
type EventSequenceIn func(platform.Tx) EventSequence

// EventAppender appends conversation's encoded events in the caller's
// transaction, so each commits with its row and sequence.
type EventAppender interface {
	Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error
}

// EventAppenderIn binds realtime's appender to the caller's transaction.
type EventAppenderIn func(platform.Tx) EventAppender

// Notifier records the latest committed event sequence for an organisation,
// so open streams wake; posting and branching call it only after commit.
type Notifier interface {
	Raise(organizationID kernel.ID, seq int64)
}

// SnapshotRunner runs the page snapshot, One and Many (decision 26): the use
// case owns the read boundary, and the stores and directories only bind to
// the Snapshot they are given, so every read sees the same committed state.
type SnapshotRunner interface {
	// InSnapshot runs fn in a new read-only, repeatable-read transaction,
	// returning fn's error as is.
	InSnapshot(ctx context.Context, fn func(platform.Snapshot) error) error
}

// ReadStore is conversation's reads in the caller's snapshot: the channel
// reads are scoped by organisation, the others by organisation and channel.
// It has no write method, so a snapshot never carries a write.
type ReadStore interface {
	// GetMessages returns the requested IDs of the channel only, newest
	// first; missing or out-of-scope IDs are omitted.
	GetMessages(ctx context.Context, organizationID, channelID kernel.ID, ids []kernel.ID) ([]Message, error)
	// ListMessagesBefore returns at most limit messages of the channel older
	// than event_seq before (the latest when before is nil), newest first,
	// only of topicID when it is set.
	ListMessagesBefore(ctx context.Context, organizationID, channelID kernel.ID, topicID *kernel.ID, before *int64, limit int32) ([]Message, error)
	// GetMessage returns the channel's message at eventSeq, or
	// ErrMessageNotFound when the scoped key is absent.
	GetMessage(ctx context.Context, organizationID, channelID kernel.ID, eventSeq int64) (Message, error)
	// LookupTopics resolves the requested topics of the channel in one batch,
	// so a page costs one topic query rather than one per message. Missing
	// and out-of-scope topics are omitted.
	LookupTopics(ctx context.Context, organizationID, channelID kernel.ID, topicIDs []kernel.ID) (map[kernel.ID]Topic, error)
	// GetChannel returns a channel of the organisation, or ErrChannelNotFound.
	GetChannel(ctx context.Context, organizationID, id kernel.ID) (Channel, error)
	// ListChannels returns the organisation's channels by name, then ID.
	ListChannels(ctx context.Context, organizationID kernel.ID) ([]Channel, error)
	// GetTopic returns a topic of the channel, or ErrTopicNotFound.
	GetTopic(ctx context.Context, organizationID, channelID, id kernel.ID) (Topic, error)
	// ListTopics returns at most limit topics of the channel: the default
	// first, then by name ignoring case, then by ID. A limit outside
	// 1…math.MaxInt32 is an error.
	ListTopics(ctx context.Context, organizationID, channelID kernel.ID, limit int) ([]Topic, error)
}

// ReadStoreIn binds conversation's reads to the caller's snapshot.
type ReadStoreIn func(platform.Snapshot) ReadStore

// EventCursor is what the latest page needs from org: the organisation's
// committed event_seq in the page's snapshot, where its stream starts. An
// unknown organisation is org.ErrNotFound.
type EventCursor interface {
	EventSeq(ctx context.Context, organizationID kernel.ID) (int64, error)
}

// EventCursorIn binds org's cursor to the caller's snapshot, the one the
// page is read in.
type EventCursorIn func(platform.Snapshot) EventCursor
