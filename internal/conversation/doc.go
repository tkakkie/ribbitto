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
// Reader and NewReader own Page and Before (a ChannelPage), One an Entry
// by event_seq and Many a
// bounded ID batch, all scoped by organisation and channel, over History and
// TopicDirectory (one topic batch per page); PageSize and ChannelPage, the
// page snapshot's result; MemberDirectoryIn and AccountDirectoryIn, the
// reader's author lookups from org and identity, bound to the caller's
// snapshot by closures in cmd/* and the tests, like EventCursorIn, org's
// committed event_seq for the latest page; TxRunner, Writer with
// WriterIn, EventSequenceIn, EventAppenderIn and Notifier, the transaction
// ports Posting and Brancher own their transactions through (the store
// backs CreateTopic and MoveMessages with copies of the legacy queries until
// step 4.16);
// SnapshotRunner, which owns Reader's snapshot, and ReadStore with
// ReadStoreIn, which binds its channel, topic and message reads to that
// snapshot (the store's message queries, InsertMessage, GetMessage,
// ListMessagesBefore and GetMessages, are copies of the legacy ones until
// step 4.15, and ListTopics and LookupTopics until 4.16); Posting, NewPosting,
// Post and PostToTopic, which validate and
// commit a post with its event, then notify; Brancher, NewBrancher, Branch,
// MaxBranchMessages, ErrInvalidBranch and ErrBranchConflict, which validate
// and commit a move and notice with both events, then notify; the event kinds
// conversation publishes and so owns.
// KindPosted with Posted, EncodePosted, DecodePosted and RoutePosted is
// message.posted, its payload and its routing; KindMessagesMoved with Moved,
// EncodeMoved, DecodeMoved and RouteMoved is messages.moved. conversationpg
// registers both routers with realtime's reader.
package conversation
