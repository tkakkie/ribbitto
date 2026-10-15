-- +goose Up
ALTER TABLE message
  ADD COLUMN pinned_by_member_id uuid,
  ADD COLUMN pinned_at timestamptz,
  ADD CONSTRAINT message_pin_pair_check
    CHECK ((pinned_by_member_id IS NULL) = (pinned_at IS NULL)),
  ADD CONSTRAINT message_pinner_fkey
    FOREIGN KEY (organization_id, pinned_by_member_id)
    REFERENCES member (organization_id, id) ON DELETE RESTRICT;
CREATE INDEX message_channel_pin_idx
  ON message (organization_id, channel_id, pinned_at DESC, id)
  WHERE pinned_at IS NOT NULL;

-- +goose Down
DROP INDEX message_channel_pin_idx;
ALTER TABLE message DROP COLUMN pinned_at, DROP COLUMN pinned_by_member_id;
