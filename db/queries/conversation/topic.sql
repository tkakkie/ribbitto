-- name: GetTopic :one
-- Duplicates the legacy query in db/queries/topic.sql, which step 4.16 deletes.
-- The organisation and channel predicates are the read's scope check.
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: GetDefaultTopic :one
-- Duplicates the legacy query in db/queries/topic.sql, which step 4.16 deletes.
SELECT * FROM topic WHERE organization_id = $1 AND channel_id = $2 AND is_default;
