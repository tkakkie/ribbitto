-- name: InsertMessage :one
INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: GetMessage :one
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND event_seq = $3;

-- name: ListMessagesBefore :many
SELECT * FROM message
WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
  AND (sqlc.narg(before_event_seq)::bigint IS NULL OR event_seq < sqlc.narg(before_event_seq)::bigint)
ORDER BY event_seq DESC
LIMIT sqlc.arg('limit');
