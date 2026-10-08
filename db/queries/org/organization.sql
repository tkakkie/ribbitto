-- name: GetOrganizationBySlug :one
SELECT id, slug, name, event_seq, created_at, event_log_boundary_seq, access_epoch FROM organization WHERE slug = $1;
