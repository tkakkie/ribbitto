-- name: CreateSession :one
INSERT INTO session (token_hash, account_id, expires_at) VALUES ($1, $2, $3) RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT sqlc.embed(session), account.id, account.email, account.display_name
FROM session JOIN account ON account.id = session.account_id
WHERE session.token_hash = $1 AND session.expires_at > sqlc.arg(now);

-- name: DeleteSessionByTokenHash :exec
DELETE FROM session WHERE token_hash = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM session WHERE expires_at < $1;
