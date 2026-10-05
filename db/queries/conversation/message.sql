-- name: InsertMessage :one
-- Duplicates the legacy query in db/queries/message.sql, which step 4.15 deletes.
INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: GetMessage :one
-- Duplicates the legacy query in db/queries/message.sql, which step 4.15
-- deletes. The snapshot tests find statements by name, so it keeps its own.
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND event_seq = $3;

-- name: ListMessagesBefore :many
-- Duplicates the legacy query in db/queries/message.sql, which step 4.15 deletes.
SELECT * FROM message
WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
  AND (sqlc.narg(topic_id)::uuid IS NULL OR topic_id = sqlc.narg(topic_id)::uuid)
  AND (sqlc.narg(before_event_seq)::bigint IS NULL OR event_seq < sqlc.narg(before_event_seq)::bigint)
ORDER BY event_seq DESC
LIMIT sqlc.arg('limit');

-- name: GetMessages :many
-- Duplicates the legacy query in db/queries/message.sql, which step 4.15 deletes.
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND id = ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY event_seq DESC;
