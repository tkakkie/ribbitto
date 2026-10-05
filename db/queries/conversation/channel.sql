-- name: CreateChannel :one
-- Conversation owns both tables: creating a channel also writes its default
-- topic, so a channel never exists without one (decision 21). One statement
-- keeps both in one transaction even on a pool; the channel's foreign key to
-- the topic is deferred to commit.
WITH created AS (
  INSERT INTO channel (organization_id, name, is_default)
  VALUES ($1, $2, $3) RETURNING *
), default_topic AS (
  INSERT INTO topic (id, organization_id, channel_id, is_default)
  SELECT default_topic_id, organization_id, id, true FROM created
)
SELECT * FROM created;

-- name: ListChannels :many
SELECT * FROM channel WHERE organization_id = $1 ORDER BY name, id;

-- name: GetChannel :one
SELECT * FROM channel WHERE organization_id = $1 AND id = $2;

-- name: GetDefaultChannel :one
SELECT * FROM channel WHERE organization_id = $1 AND is_default;
