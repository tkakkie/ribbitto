-- +goose Up
ALTER TABLE organization ADD COLUMN event_log_boundary_seq bigint NOT NULL DEFAULT 0;
-- Existing sequences have no log rows; replay starts after this boundary.
UPDATE organization SET event_log_boundary_seq = event_seq;

CREATE TABLE event_log (
  organization_id    uuid        NOT NULL REFERENCES organization(id) ON DELETE RESTRICT,
  seq                bigint      NOT NULL CHECK (seq >= 1),
  kind               text        NOT NULL,
  audience_member_id uuid,
  data               jsonb       NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, seq),
  FOREIGN KEY (organization_id, audience_member_id) REFERENCES member (organization_id, id) ON DELETE RESTRICT
);

-- +goose Down
DROP TABLE event_log;
ALTER TABLE organization DROP COLUMN event_log_boundary_seq;
