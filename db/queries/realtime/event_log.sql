-- name: EventsAfter :many
-- The caller reads org's cursor bounds first, in the same snapshot.
SELECT seq, kind, audience_member_id, data FROM event_log
WHERE organization_id = sqlc.arg(organization_id) AND seq > sqlc.arg(after_seq)
ORDER BY seq LIMIT sqlc.arg(batch_limit)::bigint;

-- name: InsertEvent :exec
-- The publisher encoded data; realtime stores it without knowing the kind.
INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
VALUES (sqlc.arg(organization_id), sqlc.arg(seq), sqlc.arg(kind), sqlc.narg(audience_member_id), sqlc.arg(data)::jsonb);
