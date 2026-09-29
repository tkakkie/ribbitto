-- name: CreateAccount :one
INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, $3) RETURNING *;

-- name: GetAccountByEmail :one
SELECT * FROM account WHERE email = $1;

-- name: GetAccountByID :one
SELECT * FROM account WHERE id = $1;

-- name: LookupDisplayNames :many
SELECT id, display_name FROM account WHERE id = ANY(sqlc.arg(account_ids)::uuid[]);
