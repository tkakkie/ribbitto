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

-- Every sequence taken above the boundary must commit with its event row.
-- Checked at commit, so a writer that predates this migration (a server
-- still running the old binary while `migrate up` runs) fails and rolls its
-- sequence back instead of leaving a permanent gap in the log.
-- +goose StatementBegin
CREATE FUNCTION organization_event_seq_logged() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Every sequence this update took above the boundary, not only the last:
  -- an update may advance the counter by more than one.
  IF NEW.event_seq > GREATEST(OLD.event_seq, NEW.event_log_boundary_seq)
     AND (SELECT count(*) FROM event_log
          WHERE organization_id = NEW.id
            AND seq > GREATEST(OLD.event_seq, NEW.event_log_boundary_seq)
            AND seq <= NEW.event_seq)
         <> NEW.event_seq - GREATEST(OLD.event_seq, NEW.event_log_boundary_seq) THEN
    RAISE EXCEPTION 'event_seq % of organization % has sequences without event_log rows', NEW.event_seq, NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER organization_event_seq_logged
  AFTER UPDATE OF event_seq ON organization
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION organization_event_seq_logged();

-- +goose Down
DROP TRIGGER organization_event_seq_logged ON organization;
DROP FUNCTION organization_event_seq_logged();
DROP TABLE event_log;
ALTER TABLE organization DROP COLUMN event_log_boundary_seq;
