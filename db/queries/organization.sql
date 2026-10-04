-- name: GetEventSeq :one
-- A copy of org's GetEventSeq (db/queries/org/), until 3.12.
SELECT event_seq FROM organization WHERE id = $1;

-- name: NextEventSeq :one
-- Setup and sign-up's copy of org's NextEventSeq (db/queries/org/), until 3.12.
UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq;
