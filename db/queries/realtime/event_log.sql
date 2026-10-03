-- name: EventsAfter :many
-- The caller reads org's cursor bounds first, in the same snapshot.
SELECT seq, kind, audience_member_id, data FROM event_log
WHERE organization_id = sqlc.arg(organization_id) AND seq > sqlc.arg(after_seq)
ORDER BY seq LIMIT sqlc.arg(batch_limit)::bigint;
