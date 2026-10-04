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
// MemberDirectoryIn and AccountDirectoryIn, the page reader's author lookups
// from org and identity, bound to the caller's snapshot by closures in cmd/*
// and the tests; TxRunner, Writer with WriterIn, EventSequenceIn,
// EventAppenderIn and Notifier, the transaction ports posting and branching
// will own their transaction through (unused until steps 4.9 and 4.10a); and
// the two event kinds conversation publishes and so owns.
// KindPosted with Posted, EncodePosted, DecodePosted and RoutePosted is
// message.posted, its payload and its routing; KindMessagesMoved with Moved,
// EncodeMoved, DecodeMoved and RouteMoved is messages.moved. conversationpg
// registers both routers with realtime's reader.
package conversation
