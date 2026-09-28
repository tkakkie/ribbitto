-- +goose Up
-- Every organisation has exactly one default channel. Setup creates it from
-- now on; organisations set up before that get one here.

-- An organisation without a default that already has a channel named
-- "general" makes that channel the default: inserting a second "general"
-- would break the unique name.
UPDATE channel
SET is_default = true
WHERE name = 'general'
  AND NOT EXISTS (SELECT FROM channel d WHERE d.organization_id = channel.organization_id AND d.is_default);

INSERT INTO channel (organization_id, name, is_default)
SELECT o.id, 'general', true
FROM organization o
WHERE NOT EXISTS (SELECT FROM channel c WHERE c.organization_id = o.id AND c.is_default);

-- +goose Down
-- Nothing to undo: the default channels stay, since messages may already
-- refer to them, and 00005's down removes the whole table anyway.
SELECT 1;
