-- name: CreateMember :one
INSERT INTO member (organization_id, account_id, role, joined_event_seq)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetMemberByOrganizationAndAccount :one
SELECT * FROM member WHERE organization_id = $1 AND account_id = $2;

-- name: GetMembershipBySlug :one
SELECT o.id AS organization_id, o.slug, o.name, m.id AS member_id, m.role
FROM organization o
JOIN member m ON m.organization_id = o.id
WHERE o.slug = $1 AND m.account_id = $2;

-- name: GetHomeSlug :one
SELECT o.slug
FROM setup s
JOIN organization o ON o.id = s.organization_id
JOIN member m ON m.organization_id = o.id
WHERE m.account_id = $1;
