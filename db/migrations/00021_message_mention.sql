-- +goose Up
CREATE TABLE message_mention (
  organization_id uuid   NOT NULL,
  message_id      uuid   NOT NULL,
  member_id       uuid   NOT NULL,
  event_seq       bigint NOT NULL CHECK (event_seq >= 1),
  PRIMARY KEY (organization_id, message_id, member_id),
  CONSTRAINT message_mention_message_fkey
    FOREIGN KEY (organization_id, message_id)
    REFERENCES message (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT message_mention_member_fkey
    FOREIGN KEY (organization_id, member_id)
    REFERENCES member (organization_id, id) ON DELETE RESTRICT
);
CREATE INDEX message_mention_member_event_seq_idx
  ON message_mention (organization_id, member_id, event_seq DESC);

-- +goose Down
DROP TABLE message_mention;
