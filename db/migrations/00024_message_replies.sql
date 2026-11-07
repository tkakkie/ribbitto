-- +goose Up
CREATE INDEX message_replies_idx ON message (organization_id, reply_to_member_id, event_seq DESC, channel_id)
  WHERE reply_to_member_id IS NOT NULL;

-- +goose Down
DROP INDEX message_replies_idx;
