-- name: SetupOpen :one
SELECT NOT EXISTS (SELECT 1 FROM setup);

-- name: CompleteSetup :exec
INSERT INTO setup (id, organization_id) VALUES (true, $1);

-- name: CreateOrganization :one
INSERT INTO organization (name, slug) VALUES ($1, $2) RETURNING *;

-- name: SetupOrganization :one
SELECT organization_id FROM setup WHERE id;
