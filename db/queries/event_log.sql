-- name: InsertMessageEvent :exec
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES ($1, $2, $3, NULL, jsonb_build_object('channel_id', sqlc.arg(channel_id)::uuid, 'message_id', sqlc.arg(message_id)::uuid));

-- name: InsertMemberEvent :exec
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES ($1, $2, $3, NULL, jsonb_build_object('member_id', sqlc.arg(member_id)::uuid));

-- name: EventsAfter :many
SELECT organization_id, seq, kind, audience_member_id, data
FROM event_log
WHERE organization_id = $1 AND seq > sqlc.arg(after_seq)
ORDER BY seq
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: CommittedSequences :many
SELECT id, event_seq FROM organization WHERE id = ANY(sqlc.arg(organization_ids)::uuid[]);
