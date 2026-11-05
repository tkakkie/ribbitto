-- name: InsertMessageMentions :exec
INSERT INTO message_mention (organization_id, message_id, member_id, event_seq)
SELECT sqlc.arg(organization_id), sqlc.arg(message_id), member_id, sqlc.arg(event_seq)
FROM unnest(sqlc.arg(member_ids)::uuid[]) AS members(member_id);

-- name: ListMentions :many
SELECT m.*
FROM message_mention r
JOIN message m ON m.organization_id = r.organization_id AND m.id = r.message_id
JOIN channel c ON c.organization_id = m.organization_id AND c.id = m.channel_id
WHERE r.organization_id = sqlc.arg(organization_id)
  AND m.organization_id = sqlc.arg(organization_id)
  AND c.organization_id = sqlc.arg(organization_id)
  AND r.member_id = sqlc.arg(member_id)
  AND (sqlc.narg(before_event_seq)::bigint IS NULL OR r.event_seq < sqlc.narg(before_event_seq))
  AND c.id = ANY(sqlc.arg(channel_ids)::uuid[])
  AND NOT (m.member_id = sqlc.arg(member_id))
ORDER BY r.event_seq DESC
LIMIT sqlc.arg(row_limit);
