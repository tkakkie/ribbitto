-- name: CreateChannel :one
INSERT INTO channel (organization_id, name, is_default)
VALUES ($1, $2, $3) RETURNING *;

-- name: ListChannels :many
SELECT * FROM channel WHERE organization_id = $1 ORDER BY name, id;

-- name: GetChannel :one
SELECT * FROM channel WHERE organization_id = $1 AND id = $2;

-- name: GetDefaultChannel :one
SELECT * FROM channel WHERE organization_id = $1 AND is_default;
