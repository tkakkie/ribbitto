-- name: CreateMember :one
-- A copy of org's CreateMember (db/queries/org/), until 3.12.
INSERT INTO member (organization_id, account_id, role, joined_event_seq, handle)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetMemberByOrganizationAndAccount :one
SELECT * FROM member WHERE organization_id = $1 AND account_id = $2;
