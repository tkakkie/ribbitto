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

-- name: ExpireEvents :exec
-- Lock organisations before events, as posting does. Deletion and the
-- boundary advance share one transaction, including concurrent cleaners.
WITH locked AS MATERIALIZED (
    SELECT id FROM organization o
    WHERE EXISTS (SELECT 1 FROM event_log e
                  WHERE e.organization_id = o.id AND e.created_at < sqlc.arg(cutoff))
    ORDER BY id FOR UPDATE
), deleted AS (
    DELETE FROM event_log e USING locked o
    WHERE e.organization_id = o.id AND e.created_at < sqlc.arg(cutoff)
    RETURNING e.organization_id, e.seq
)
UPDATE organization o
SET event_log_boundary_seq = greatest(o.event_log_boundary_seq, d.seq)
FROM (SELECT organization_id, max(seq) AS seq FROM deleted GROUP BY organization_id) d
WHERE o.id = d.organization_id;

-- name: CommittedSequences :many
SELECT id, event_seq FROM organization WHERE id = ANY(sqlc.arg(organization_ids)::uuid[]);
