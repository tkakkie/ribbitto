-- name: EventsAfter :many
-- The caller reads org's cursor bounds first, in the same snapshot.
SELECT seq, kind, audience_member_id, data FROM event_log
WHERE organization_id = sqlc.arg(organization_id) AND seq > sqlc.arg(after_seq)
ORDER BY seq LIMIT sqlc.arg(batch_limit)::bigint;

-- name: InsertEvent :exec
-- The publisher encoded data; realtime stores it without knowing the kind.
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES (sqlc.arg(organization_id), sqlc.arg(seq), sqlc.arg(kind), sqlc.narg(audience_member_id), sqlc.arg(data)::jsonb);

-- name: OrganizationsWithExpiredEvents :many
SELECT DISTINCT organization_id FROM event_log WHERE created_at < sqlc.arg(cutoff) ORDER BY organization_id;

-- name: LowestEvents :many
-- The cleaner reads these after taking the organisation lock, so it sees
-- committed progress by concurrent cleaners; 1,000 bounds the work under it.
SELECT seq, created_at FROM event_log
WHERE organization_id = sqlc.arg(organization_id)
ORDER BY seq LIMIT 1000;

-- name: DeleteEventsThrough :one
WITH deleted AS (
    DELETE FROM event_log
    WHERE organization_id = sqlc.arg(organization_id) AND seq <= sqlc.arg(through)::bigint
    RETURNING seq
)
SELECT count(*)::bigint AS deleted, coalesce(max(seq), 0)::bigint AS through FROM deleted;
