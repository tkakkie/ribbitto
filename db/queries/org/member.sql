-- name: GetMembershipBySlug :one
SELECT o.id AS organization_id, o.slug, o.name, m.id AS member_id, m.role, m.handle
FROM organization o
JOIN member m ON m.organization_id = o.id
WHERE o.slug = $1 AND m.account_id = $2;

-- name: GetHomeSlug :one
SELECT o.slug
FROM setup s
JOIN organization o ON o.id = s.organization_id
JOIN member m ON m.organization_id = o.id
WHERE m.account_id = $1;

-- name: UpdateMemberHandle :execrows
UPDATE member SET handle = $3 WHERE organization_id = $1 AND id = $2;

-- name: LookupMembers :many
SELECT id, account_id, handle FROM member
WHERE organization_id = $1 AND id = ANY(sqlc.arg(member_ids)::uuid[]);
