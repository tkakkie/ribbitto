-- name: GetOrganizationBySlug :one
SELECT id, slug, name, event_seq, created_at FROM organization WHERE slug = $1;
