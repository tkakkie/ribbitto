-- +goose Up
-- Topics inside channels (decision 21, docs/domain/topics.md). This only
-- adds the table: channel.default_topic_id and message.topic_id, their
-- backfill and NOT NULL arrive together with the write paths (#307), so
-- channel creation and posting keep working until then.
CREATE TABLE topic (
  id              uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid        NOT NULL,
  channel_id      uuid        NOT NULL,
  -- NULL only for the default topic, whose label comes from the message files.
  name            text        CONSTRAINT topic_name_check CHECK (length(name) BETWEEN 1 AND 80 AND name = normalize(name, NFC)),
  is_default      boolean     NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT topic_default_unnamed_check CHECK (is_default = (name IS NULL)),
  FOREIGN KEY (organization_id, channel_id) REFERENCES channel (organization_id, id) ON DELETE RESTRICT,
  -- Targets for #307's composite foreign keys: message (organization_id,
  -- channel_id, topic_id) can only name a topic of its own channel, and
  -- channel (…, default_topic_id, default_topic_is_default) only that
  -- channel's default topic.
  CONSTRAINT topic_channel_id_key UNIQUE (organization_id, channel_id, id),
  CONSTRAINT topic_channel_id_default_key UNIQUE (organization_id, channel_id, id, is_default)
);
-- A plain unique index would not limit defaults: their NULL names never collide.
CREATE UNIQUE INDEX topic_default_idx ON topic (organization_id, channel_id) WHERE is_default;
CREATE UNIQUE INDEX topic_name_idx ON topic (organization_id, channel_id, lower(name)) WHERE name IS NOT NULL;

-- +goose Down
DROP TABLE topic;
