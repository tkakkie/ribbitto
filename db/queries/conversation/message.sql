-- name: InsertMessage :one
-- Duplicates the legacy query in db/queries/message.sql, which step 4.15 deletes.
INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;
