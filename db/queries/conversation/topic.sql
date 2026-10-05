-- name: GetTopic :one
-- The organisation and channel predicates are the read's scope check.
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: GetDefaultTopic :one
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND is_default;

-- name: CreateTopic :one
INSERT INTO topic (organization_id, channel_id, name, is_default)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: MoveMessages :execrows
-- Branching moves conversation's own messages by writing message.topic_id.
-- Only messages still in the expected topic move; the caller compares the
-- count with the selection and rolls back on a mismatch (409).
UPDATE message SET topic_id = sqlc.arg(to_topic_id)
WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
  AND topic_id = sqlc.arg(from_topic_id) AND id = ANY(sqlc.arg(message_ids)::uuid[]);

-- name: ListTopics :many
SELECT * FROM topic
WHERE organization_id = $1 AND channel_id = $2
ORDER BY is_default DESC, lower(name), id
LIMIT $3;

-- name: LookupTopics :many
SELECT * FROM topic
WHERE organization_id = $1 AND channel_id = $2 AND id = ANY(sqlc.arg(topic_ids)::uuid[]);
