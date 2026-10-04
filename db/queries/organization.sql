-- name: GetOrganizationBySlug :one
SELECT id, slug, name, event_seq, created_at, event_log_boundary_seq FROM organization WHERE slug = $1;

-- name: NextEventSeq :one
-- Setup and sign-up's copy of org's NextEventSeq (db/queries/org/), until 3.12.
UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq;
