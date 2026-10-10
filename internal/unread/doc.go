// Package unread owns channel_read, read_range and topic_read_floor
// (decision 32). Read ranges name sequences so moving messages cannot change
// read state. A per-member, per-channel lock serialises unions; a join prefix
// makes pre-membership messages read. This module never reads message.
package unread
