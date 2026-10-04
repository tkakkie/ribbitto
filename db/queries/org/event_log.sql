-- name: EventBounds :one
-- realtime's reader reads org's cursor bounds through this, in its snapshot.
SELECT event_log_boundary_seq, event_seq FROM organization WHERE id = $1;

-- name: LockEventRetentionOrganization :exec
SELECT id FROM organization WHERE id = $1 FOR UPDATE;

-- name: RaiseEventLogBoundary :exec
-- greatest: the boundary never goes down (realtime.RetentionBoundary).
UPDATE organization SET event_log_boundary_seq = greatest(event_log_boundary_seq, sqlc.arg(through)::bigint)
WHERE id = sqlc.arg(organization_id);

-- name: CommittedSequences :many
SELECT id, event_seq FROM organization WHERE id = ANY(sqlc.arg(organization_ids)::uuid[]);

-- name: NextEventSeq :one
-- Posting, branching, setup and sign-up take this first: the row lock makes sequence order
-- commit order (decision 5).
UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq;

-- name: GetEventSeq :one
-- The latest page's cursor, read in the page's snapshot.
SELECT event_seq FROM organization WHERE id = $1;
