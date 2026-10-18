-- +goose Up
-- Case-only twins are the same user-facing name (maintainer ruling #1289).
-- Existing twins must make index creation fail and roll back this transaction;
-- the operator chooses which channel to rename before retrying the upgrade.
ALTER TABLE channel DROP CONSTRAINT channel_organization_id_name_key;
CREATE UNIQUE INDEX channel_name_idx ON channel (organization_id, lower(name));

-- +goose Down
DROP INDEX channel_name_idx;
ALTER TABLE channel ADD CONSTRAINT channel_organization_id_name_key UNIQUE (organization_id, name);
