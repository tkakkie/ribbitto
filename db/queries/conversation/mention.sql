-- name: InsertMessageMentions :exec
INSERT INTO message_mention (organization_id, message_id, member_id, event_seq)
SELECT sqlc.arg(organization_id), sqlc.arg(message_id), member_id, sqlc.arg(event_seq)
FROM unnest(sqlc.arg(member_ids)::uuid[]) AS members(member_id);
