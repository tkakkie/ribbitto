-- name: GetTopic :one
-- The organisation and channel predicates are the read's scope check.
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: GetDefaultTopic :one
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND is_default;

-- name: CreateTopic :one
INSERT INTO topic (organization_id, channel_id, name, is_default)
VALUES ($1, $2, $3, false) RETURNING *;

-- name: MoveMessages :execrows
-- Branching records the destination and latest move together.
-- Only messages still in the expected topic move; the caller compares the
-- count with the selection and rolls back on a mismatch (409).
UPDATE message SET topic_id = sqlc.arg(to_topic_id), moved_event_seq = sqlc.arg(moved_event_seq)
WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
  AND topic_id = sqlc.arg(from_topic_id) AND id = ANY(sqlc.arg(message_ids)::uuid[]);

-- name: ListTopics :many
-- The default topic first, then open before resolved, then by name and ID.
SELECT * FROM topic
WHERE organization_id = $1 AND channel_id = $2
ORDER BY is_default DESC, (conclusion_message_id IS NOT NULL), lower(name), id
LIMIT $3;

-- name: LookupTopics :many
SELECT * FROM topic
WHERE organization_id = $1 AND channel_id = $2 AND id = ANY(sqlc.arg(topic_ids)::uuid[]);

-- name: SetTopicConclusion :execrows
UPDATE topic SET conclusion_message_id = sqlc.narg(conclusion_message_id)
WHERE organization_id = $1 AND channel_id = $2 AND id = $3;
