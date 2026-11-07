-- name: ListReplies :many
SELECT * FROM message
WHERE organization_id = sqlc.arg(organization_id)
  AND reply_to_member_id = sqlc.arg(member_id)
  AND (sqlc.narg(before_event_seq)::bigint IS NULL OR event_seq < sqlc.narg(before_event_seq))
  AND channel_id = ANY(sqlc.arg(channel_ids)::uuid[])
  AND NOT (member_id = sqlc.arg(member_id))
ORDER BY event_seq DESC
LIMIT sqlc.arg(row_limit);
