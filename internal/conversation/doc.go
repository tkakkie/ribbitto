// Package conversation is the conversation module's root (decisions 26 and
// 27): channels, topics, branching, posting, history and the page snapshot.
// IDs are kernel.ID; the package never imports domain.
//
// Feature: conversation (feature map in docs/architecture/features.md), which
// owns the channel, topic and message tables. Exported API: Channel, its name
// rule, errors and default name; Channels (List, Create, Get, Default),
// NewChannels and ChannelStore; Topic, ValidateTopicName, ErrTopicNotFound,
// ErrInvalidTopicName and ErrTopicNameTaken; Topics, NewTopics and
// TopicReader, the topic lookup scoped by a membership that web's stream and
// paging links use; Message, ValidateMessageBody, ErrInvalidBody and
// ErrMessageNotFound.
//
// Reader and NewReader own Page and Before (a ChannelPage), One an Entry by
// event_seq and Many a bounded ID batch, all scoped by organisation and
// channel, over History and TopicDirectory (one topic batch per page);
// PageSize is the history page's limit and ChannelPage the page snapshot's
// result. SnapshotRunner
// owns Reader's snapshot, and ReadStore with ReadStoreIn binds its channel,
// topic and message reads to it. MemberDirectoryIn and AccountDirectoryIn,
// the reader's author lookups from org and identity, and EventCursorIn, org's
// committed event_seq for the latest page, are bound to that snapshot by
// closures in cmd/* and the tests. The member lookup returns org's
// DirectoryEntry, not a copy of it.
//
// Posting, NewPosting, Post and PostToTopic validate and commit a post with
// its event, then notify; Brancher, NewBrancher, Branch, MaxBranchMessages,
// ErrInvalidBranch and ErrBranchConflict validate and commit a move and notice
// with both events, then notify. Both own their transaction through TxRunner,
// with Writer and WriterIn, EventSequenceIn, EventAppenderIn and Notifier.
//
// KindPosted with Posted, EncodePosted, DecodePosted and RoutePosted is
// message.posted, its payload and its routing; KindMessagesMoved with Moved,
// EncodeMoved, DecodeMoved and RouteMoved is messages.moved. Conversation
// publishes both kinds and so owns them.
//
// Conversation's store implements ChannelStore, TopicReader, Writer, ReadStore
// and setup's default-channel write. conversationpg builds the use cases,
// binds the store to the caller's transaction or snapshot, implements
// TxRunner and SnapshotRunner over the pool, and registers both routers with
// realtime's reader.
package conversation
