-- name: GetOrganizationBySlug :one
SELECT id, slug, name, event_seq, created_at FROM organization WHERE slug = $1;

-- name: GetEventSeq :one
SELECT event_seq FROM organization WHERE id = $1;

-- name: NextEventSeq :one
UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq;
