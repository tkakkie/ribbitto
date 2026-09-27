-- name: CreateAccount :one
INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, $3) RETURNING *;

-- name: GetAccountByEmail :one
SELECT * FROM account WHERE email = $1;

-- name: GetAccountByID :one
SELECT * FROM account WHERE id = $1;
