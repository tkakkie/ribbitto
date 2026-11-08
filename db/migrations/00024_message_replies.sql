-- +goose Up
-- Defer a reply-link foreign-key index until message deletion exists: with
-- ON DELETE RESTRICT, the referenced-side probe is not needed before deletion work.
CREATE INDEX message_replies_idx ON message (organization_id, reply_to_member_id, event_seq DESC, channel_id)
  WHERE reply_to_member_id IS NOT NULL;

-- +goose Down
DROP INDEX message_replies_idx;
