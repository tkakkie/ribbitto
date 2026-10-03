-- Temporary (decision 26, until migration step 3): setup and sign-up write
-- identity's account table inside their own transactions, so they keep
-- these copies of identity's queries on the legacy sqlc entry until org
-- becomes a module and calls identity instead.

-- name: CreateAccount :one
INSERT INTO account (email, display_name, password_hash) VALUES ($1, $2, $3) RETURNING *;

-- name: GetAccountByID :one
SELECT * FROM account WHERE id = $1;
