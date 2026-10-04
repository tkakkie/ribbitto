-- name: SetupOpen :one
-- A copy of org's SetupOpen (db/queries/org/), until 3.12.
SELECT NOT EXISTS (SELECT 1 FROM setup);

-- name: CompleteSetup :exec
-- A copy of org's CompleteSetup (db/queries/org/), until 3.12.
INSERT INTO setup (id, organization_id) VALUES (true, $1);

-- name: CreateOrganization :one
-- A copy of org's CreateOrganization (db/queries/org/), until 3.12.
INSERT INTO organization (name, slug) VALUES ($1, $2) RETURNING *;
