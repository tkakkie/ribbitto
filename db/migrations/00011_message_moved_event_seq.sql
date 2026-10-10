-- +goose Up
-- Decision 32: branching records the latest move independently of the
-- message's original event_seq, which never changes.
ALTER TABLE message ADD COLUMN moved_event_seq bigint;
CREATE INDEX message_moved_event_seq_idx ON message (organization_id, topic_id, moved_event_seq)
  WHERE moved_event_seq IS NOT NULL;

-- +goose Down
ALTER TABLE message DROP COLUMN moved_event_seq;
