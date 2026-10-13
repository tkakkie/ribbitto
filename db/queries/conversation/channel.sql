-- name: LockChannelCreation :one
-- Every live organisation keeps its default channel: its stable row is the
-- creation lock without reading another module's tables. Count in the next
-- statement for a fresh snapshot after waiting for concurrent creators.
-- NO KEY UPDATE serializes creators without blocking FK checks
-- (FOR KEY SHARE) from message, topic and unread inserts.
SELECT id FROM channel WHERE organization_id = $1 AND is_default FOR NO KEY UPDATE;

-- name: CreateChannel :one
-- Conversation owns both tables: creating a channel also writes its default
-- topic, so a channel never exists without one (decision 21). One statement
-- keeps both in one transaction even on a pool; the channel's foreign key to
-- the topic is deferred to commit.
WITH capacity AS (
  SELECT count(*) AS total FROM channel c WHERE c.organization_id = $1
), created AS (
  INSERT INTO channel (organization_id, name, is_default, image)
  SELECT $1, $2, $3, sqlc.narg(image)
  FROM capacity WHERE total < sqlc.arg(max_channels)::bigint
  RETURNING *
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

-- name: SetChannelImage :execrows
UPDATE channel SET image = sqlc.narg(image)
WHERE organization_id = $1 AND id = $2;
