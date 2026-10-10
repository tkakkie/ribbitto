-- +goose Up
-- Decision 32: read state belongs to unread, independently of message moves.
CREATE TABLE channel_read (
  organization_id uuid NOT NULL,
  channel_id uuid NOT NULL,
  member_id uuid NOT NULL,
  PRIMARY KEY (organization_id, channel_id, member_id),
  FOREIGN KEY (organization_id, channel_id) REFERENCES channel (organization_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (organization_id, member_id) REFERENCES member (organization_id, id) ON DELETE RESTRICT
);
CREATE TABLE read_range (
  organization_id uuid NOT NULL,
  channel_id uuid NOT NULL,
  member_id uuid NOT NULL,
  lo bigint NOT NULL CHECK (lo >= 0),
  hi bigint NOT NULL CHECK (hi > lo),
  PRIMARY KEY (organization_id, channel_id, member_id, lo),
  FOREIGN KEY (organization_id, channel_id, member_id) REFERENCES channel_read ON DELETE CASCADE
);
CREATE TABLE topic_read_floor (
  organization_id uuid NOT NULL,
  topic_id uuid NOT NULL,
  member_id uuid NOT NULL,
  channel_id uuid NOT NULL,
  floor_seq bigint NOT NULL CHECK (floor_seq >= 0),
  PRIMARY KEY (organization_id, topic_id, member_id),
  FOREIGN KEY (organization_id, channel_id, topic_id) REFERENCES topic (organization_id, channel_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (organization_id, channel_id, member_id) REFERENCES channel_read ON DELETE CASCADE
);

-- +goose Down
DROP TABLE topic_read_floor;
DROP TABLE read_range;
DROP TABLE channel_read;
