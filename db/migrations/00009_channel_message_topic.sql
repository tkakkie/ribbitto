-- +goose Up
-- Every channel gets its default topic and every message a topic
-- (decision 21, docs/domain/topics.md). Backfill and NOT NULL happen in
-- this one migration, and the write paths change in the same release, so
-- no row is ever without a topic.

-- The default makes a channel name its default topic's id before the topic
-- exists: channel creation inserts both in one statement
-- (db/queries/conversation/channel.sql), and a channel inserted any other way
-- fails at commit unless its topic follows.
ALTER TABLE channel
  ADD COLUMN default_topic_id uuid DEFAULT uuidv7(),
  -- Fixed to true so the composite foreign key below can only reach the
  -- channel's default topic, never a named one.
  ADD COLUMN default_topic_is_default boolean NOT NULL DEFAULT true
    CONSTRAINT channel_default_topic_is_default_check CHECK (default_topic_is_default);
ALTER TABLE message ADD COLUMN topic_id uuid;

INSERT INTO topic (id, organization_id, channel_id, is_default)
SELECT default_topic_id, organization_id, id, true FROM channel;

UPDATE message m
SET topic_id = c.default_topic_id
FROM channel c
WHERE c.organization_id = m.organization_id AND c.id = m.channel_id;

ALTER TABLE channel ALTER COLUMN default_topic_id SET NOT NULL;
ALTER TABLE message ALTER COLUMN topic_id SET NOT NULL;

-- Channel and topic refer to each other, so the channel's key waits for
-- commit; it also makes the default topic undeletable.
ALTER TABLE channel ADD CONSTRAINT channel_default_topic_fkey
  FOREIGN KEY (organization_id, id, default_topic_id, default_topic_is_default)
  REFERENCES topic (organization_id, channel_id, id, is_default)
  ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
-- A message can only be in a topic of its own channel.
ALTER TABLE message ADD CONSTRAINT message_topic_fkey
  FOREIGN KEY (organization_id, channel_id, topic_id)
  REFERENCES topic (organization_id, channel_id, id) ON DELETE RESTRICT;
CREATE INDEX message_topic_event_seq_idx ON message (organization_id, topic_id, event_seq);

-- +goose Down
ALTER TABLE message DROP COLUMN topic_id;
ALTER TABLE channel DROP COLUMN default_topic_is_default, DROP COLUMN default_topic_id;
-- The default topics belong to these columns: a second up would collide
-- with them. Named topics are topic's own data and stay.
DELETE FROM topic WHERE is_default;
