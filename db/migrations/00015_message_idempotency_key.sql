-- +goose Up
-- A composer's key: at most one message per organisation, author and key
-- (#1155). Branching's notices and earlier messages keep NULL.
ALTER TABLE message ADD COLUMN idempotency_key uuid;
CREATE UNIQUE INDEX message_idempotency_key_idx
  ON message (organization_id, member_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX message_idempotency_key_idx;
ALTER TABLE message DROP COLUMN idempotency_key;
