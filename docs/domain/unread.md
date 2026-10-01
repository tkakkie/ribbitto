# Unread rules *(planned, M2–M4)*

- **Read position** of a member in a channel is
  `channel_member.last_read_event_seq` if the row exists, otherwise
  `member.joined_event_seq`. So messages from before a member joined are
  never unread, and a channel the member has never opened needs no row.
- **Unread count**:
  ```sql
  SELECT count(*) FROM message
  WHERE organization_id = $1 AND channel_id = $2 AND event_seq > $3;  -- $3 = read position
  ```
  backed by an index on `message (organization_id, channel_id, event_seq)`.
- **Joining an organisation is one transaction:** take the next
  `event_seq`, insert the `member` with `joined_event_seq` = that value, and
  insert the `member.joined` event with `data = {"member_id":"<uuid>"}` and
  `audience_member_id = NULL`. Setup's first member follows the same rule.
  Posting similarly pairs `message.event_seq` with `message.posted`.
  Logging starts after `organization.event_log_boundary_seq`; migration sets
  it to the current counter without backfill. Unread positions survive log retention.
- **Opening a channel for the first time** creates the `channel_member` row
  with `last_read_event_seq` = the **cursor of the snapshot that rendered the
  page**, not the latest sequence at insert time — otherwise messages
  posted after the page was read, and not yet shown, would be marked read.
- **The read position never moves backwards.** Several tabs or reordered
  requests must not lower it:
  ```sql
  INSERT INTO channel_member (organization_id, channel_id, member_id, last_read_event_seq)
  VALUES ($1, $2, $3, $4)
  ON CONFLICT (organization_id, channel_id, member_id) DO UPDATE
    SET last_read_event_seq = GREATEST(channel_member.last_read_event_seq, EXCLUDED.last_read_event_seq);
  ```

## Topics

Once topics exist ([`topics.md`](topics.md)), the unread design is chosen
in M4's unread issue, before a topic view affects read state, and must pass
both tests:

1. **Branching never changes whether a message is read.** The per-channel
   read position above passes, because branching keeps `event_seq`.
2. **Reading one topic never marks another topic's unseen messages read.**
   Topic A has messages 10 and 12, topic B has 11; reading A in the topic
   view must leave 11 unread. Advancing the per-channel position to 12
   fails this. A per-topic position alone fails test 1: a read message
   moved into a topic whose position is lower becomes unread again.

Until then, a topic's unread count is the channel's unread messages in that
topic.

## Replies

Opening or paging through a [reply chain](replies.md) leaves the read
position unchanged. A chain omits other messages in the channel, so
advancing the channel position would mark unseen messages read. Replies
are ordinary messages under the same unread rules, with no separate
unread count. This holds for the per-channel position above and for
whatever unread model M4 chooses for [topics](#topics).
