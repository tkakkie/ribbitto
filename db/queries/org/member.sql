-- name: GetMembershipBySlug :one
SELECT o.id AS organization_id, o.slug, o.name, o.access_epoch, m.id AS member_id, m.role, m.handle, m.joined_event_seq
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

-- name: CreateMember :one
INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetMemberByOrganizationAndAccount :one
SELECT * FROM member WHERE organization_id = $1 AND account_id = $2;

-- name: GetAccessEpoch :one
SELECT access_epoch FROM organization WHERE id = $1;

-- name: ListMembers :many
SELECT id, account_id, handle FROM member
WHERE organization_id = $1 AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id LIMIT sqlc.arg(page_limit);
