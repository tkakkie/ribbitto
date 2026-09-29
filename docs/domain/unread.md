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
  insert the `event_log` row. Until M3 adds `event_log`, joining takes the
  next `event_seq` but writes no `event_log` row.
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
