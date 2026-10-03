-- name: EventBounds :one
-- realtime's reader reads org's cursor bounds through this, in its snapshot.
SELECT event_log_boundary_seq, event_seq FROM organization WHERE id = $1;

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
