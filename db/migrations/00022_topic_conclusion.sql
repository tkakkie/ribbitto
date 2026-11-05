-- +goose Up
-- Shared with #2193: either migration may arrive first, and neither Down
-- may remove an index that the other's foreign key can still need.
CREATE UNIQUE INDEX IF NOT EXISTS message_org_channel_id_key ON message (organization_id, channel_id, id);
ALTER TABLE topic
  ADD COLUMN conclusion_message_id uuid,
  ADD CONSTRAINT topic_conclusion_message_fkey
    FOREIGN KEY (organization_id, channel_id, conclusion_message_id)
    REFERENCES message (organization_id, channel_id, id) ON DELETE NO ACTION,
  ADD CONSTRAINT topic_default_unresolved_check
    CHECK (NOT is_default OR conclusion_message_id IS NULL);

-- +goose Down
ALTER TABLE topic
  DROP CONSTRAINT topic_default_unresolved_check,
  DROP CONSTRAINT topic_conclusion_message_fkey,
  DROP COLUMN conclusion_message_id;
