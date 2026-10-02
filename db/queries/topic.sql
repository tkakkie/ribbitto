-- name: CreateTopic :one
INSERT INTO topic (organization_id, channel_id, name, is_default)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetTopic :one
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: GetDefaultTopic :one
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND is_default;

-- name: ListTopics :many
SELECT * FROM topic
WHERE organization_id = $1 AND channel_id = $2
ORDER BY is_default DESC, id
LIMIT $3;
