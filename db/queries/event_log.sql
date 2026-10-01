-- name: InsertMessageEvent :exec
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES ($1, $2, $3, NULL, jsonb_build_object('channel_id', sqlc.arg(channel_id)::uuid, 'message_id', sqlc.arg(message_id)::uuid));

-- name: InsertMemberEvent :exec
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES ($1, $2, $3, NULL, jsonb_build_object('member_id', sqlc.arg(member_id)::uuid));

-- name: EventsAfter :many
SELECT o.id AS organization_id, o.event_log_boundary_seq,
       coalesce(e.seq, 0)::bigint AS seq, coalesce(e.kind, '')::text AS kind,
       e.audience_member_id, e.data
FROM organization o
LEFT JOIN LATERAL (
    SELECT seq, kind, audience_member_id, data FROM event_log
    WHERE organization_id = o.id AND seq > sqlc.arg(after_seq)
    ORDER BY seq LIMIT sqlc.arg(batch_limit)::bigint
) e ON true
WHERE o.id = sqlc.arg(organization_id)
ORDER BY e.seq;

-- name: OrganizationsWithExpiredEvents :many
SELECT id FROM organization o
WHERE EXISTS (SELECT 1 FROM event_log e
              WHERE e.organization_id = o.id AND e.created_at < sqlc.arg(cutoff))
ORDER BY id;

-- name: LockEventRetentionOrganization :exec
SELECT id FROM organization WHERE id = $1 FOR UPDATE;

-- name: ExpireEventBatch :one
-- The caller already holds this organisation's row lock. Use a fresh
-- snapshot after locking so concurrent cleaners see earlier commits.
WITH deleted AS (
    DELETE FROM event_log e
    WHERE e.organization_id = sqlc.arg(organization_id) AND e.seq IN (
        SELECT candidate.seq FROM event_log candidate
        WHERE candidate.organization_id = sqlc.arg(organization_id) AND candidate.created_at < sqlc.arg(cutoff)
        ORDER BY candidate.seq LIMIT 1000
    )
    RETURNING e.seq
), advanced AS (
    UPDATE organization
    SET event_log_boundary_seq = greatest(event_log_boundary_seq, (SELECT max(seq) FROM deleted))
    WHERE id = sqlc.arg(organization_id) AND EXISTS (SELECT 1 FROM deleted)
)
SELECT count(*) FROM deleted;

-- name: CommittedSequences :many
SELECT id, event_seq FROM organization WHERE id = ANY(sqlc.arg(organization_ids)::uuid[]);
