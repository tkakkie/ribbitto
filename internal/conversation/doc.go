// Package conversation is the conversation module's root (decisions 26 and
// 27): channels, topics and messages, moving here from the layers during
// step 4 of the migration (docs/architecture/modules.md). IDs are kernel.ID;
// the package never imports domain.
//
// Exported API so far: Channel, its name rule, errors and default name;
// Channels (List, Create, Get, Default), NewChannels and ChannelStore;
// Topic, ValidateTopicName, ErrTopicNotFound, ErrInvalidTopicName and
// ErrTopicNameTaken; Topics, NewTopics and TopicReader, the topic lookup
// scoped by a membership that web's stream and paging links use;
// Message, ValidateMessageBody, ErrInvalidBody and ErrMessageNotFound;
// Reader, the history reader: Before reads a Page below an event_seq bound
// with an optional topic filter, One an Entry by event_seq and Many a
// bounded ID batch, all scoped by organisation and channel, over History and
// TopicDirectory (one topic batch per page); PageSize and ChannelPage, the
// page snapshot's result; MemberDirectoryIn and AccountDirectoryIn, the
// reader's author lookups from org and identity, bound to the caller's
// snapshot by closures in cmd/* and the tests; TxRunner, Writer with
// WriterIn, EventSequenceIn, EventAppenderIn and Notifier, the transaction
// ports Posting owns its transaction through and branching will use (unused
// in production until steps 4.9c and 4.10a; the store backs branching's
// CreateTopic and MoveMessages with copies of the legacy queries until step
// 4.16); and the
// two event kinds conversation publishes and so owns. Posting, NewPosting,
// Post and PostToTopic validate and commit a post with its event, then notify;
// unused in production beside the frozen app/message.Service until 4.9c.
// KindPosted with Posted, EncodePosted, DecodePosted and RoutePosted is
// message.posted, its payload and its routing; KindMessagesMoved with Moved,
// EncodeMoved, DecodeMoved and RouteMoved is messages.moved. conversationpg
// registers both routers with realtime's reader.
package conversation
