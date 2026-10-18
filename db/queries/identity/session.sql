-- name: CreateSession :exec
INSERT INTO session (token_hash, account_id, expires_at) VALUES ($1, $2, $3);

-- name: GetSessionByTokenHash :one
SELECT session.id AS session_id, session.expires_at, account.id, account.email, account.display_name, account.send_key
FROM session JOIN account ON account.id = session.account_id
WHERE session.token_hash = $1 AND session.expires_at > sqlc.arg(now);

-- name: DeleteSessionByTokenHash :many
DELETE FROM session WHERE token_hash = $1 RETURNING id;

-- name: DeleteOtherSessions :many
DELETE FROM session WHERE account_id = $1 AND NOT (id = $2) RETURNING id;

-- name: DeleteExpiredSessions :exec
DELETE FROM session WHERE expires_at < $1;

-- name: SessionHorizon :one
SELECT uuidv7()::uuid AS horizon;
