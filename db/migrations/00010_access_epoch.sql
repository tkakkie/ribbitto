-- +goose Up
-- Unowned so TRUNCATE RESTART IDENTITY cannot reset this installation-wide source.
CREATE SEQUENCE organization_access_epoch_seq AS bigint NO CYCLE;
ALTER TABLE organization ADD COLUMN access_epoch bigint NOT NULL DEFAULT nextval('organization_access_epoch_seq');

-- +goose StatementBegin
CREATE FUNCTION organization_access_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    NEW.access_epoch := nextval('organization_access_epoch_seq');
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id THEN
    RAISE EXCEPTION 'organization.id is immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.access_epoch < OLD.access_epoch THEN
    RAISE EXCEPTION 'access_epoch cannot decrease' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.slug IS DISTINCT FROM OLD.slug
     OR NEW.access_epoch IS DISTINCT FROM OLD.access_epoch THEN
    NEW.access_epoch := nextval('organization_access_epoch_seq');
    IF NEW.access_epoch < OLD.access_epoch THEN
      RAISE EXCEPTION 'access_epoch cannot decrease' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER organization_access_guard BEFORE INSERT OR UPDATE OF id, slug, access_epoch ON organization
  FOR EACH ROW EXECUTE FUNCTION organization_access_guard();

-- +goose StatementBegin
CREATE FUNCTION member_access_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.id IS DISTINCT FROM OLD.id THEN
      RAISE EXCEPTION 'member.id is immutable' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.role IS NOT DISTINCT FROM OLD.role
       AND NEW.organization_id IS NOT DISTINCT FROM OLD.organization_id
       AND NEW.account_id IS NOT DISTINCT FROM OLD.account_id THEN
      RETURN NULL;
    END IF;
  END IF;
  -- NULL asks the guard for a fresh value; writers lock organisations first.
  UPDATE organization SET access_epoch = NULL WHERE id = OLD.organization_id;
  IF TG_OP = 'UPDATE' AND NEW.organization_id IS DISTINCT FROM OLD.organization_id THEN
    UPDATE organization SET access_epoch = NULL WHERE id = NEW.organization_id;
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
-- No UPDATE OF column list on purpose: the function skips unchanged rows, and
-- a column-list trigger would miss a BEFORE trigger that rewrites an access
-- column during an update that names only other columns.
CREATE TRIGGER member_access_changed AFTER DELETE OR UPDATE ON member
  FOR EACH ROW EXECUTE FUNCTION member_access_changed();

-- +goose StatementBegin
CREATE FUNCTION member_access_truncated() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- TRUNCATE (including CASCADE) does not fire row deletion triggers.
  UPDATE organization SET access_epoch = NULL;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER member_access_truncated AFTER TRUNCATE ON member
  FOR EACH STATEMENT EXECUTE FUNCTION member_access_truncated();

-- +goose Down
DROP TRIGGER member_access_truncated ON member;
DROP FUNCTION member_access_truncated();
DROP TRIGGER member_access_changed ON member;
DROP FUNCTION member_access_changed();
DROP TRIGGER organization_access_guard ON organization;
DROP FUNCTION organization_access_guard();
ALTER TABLE organization DROP COLUMN access_epoch;
DROP SEQUENCE organization_access_epoch_seq;
