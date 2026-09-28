-- +goose Up
ALTER TABLE member ADD COLUMN handle text;

-- Existing members get placeholders numbered per organisation in id order:
-- collision-free, unlike a truncated id, and valid under the rules below.
UPDATE member
SET handle = 'member-' || numbered.n
FROM (
  SELECT id, row_number() OVER (PARTITION BY organization_id ORDER BY id) AS n
  FROM member
) AS numbered
WHERE member.id = numbered.id;

-- The same rules as domain.ValidateHandle, on the canonical (lower-case) form.
ALTER TABLE member
  ALTER COLUMN handle SET NOT NULL,
  ADD CONSTRAINT member_handle_format_check CHECK (handle ~ '^[a-z][a-z0-9_.-]{0,30}[a-z0-9]$'),
  ADD CONSTRAINT member_handle_reserved_check CHECK (handle NOT IN ('everyone', 'here', 'channel', 'all')),
  ADD CONSTRAINT member_organization_id_handle_key UNIQUE (organization_id, handle);

-- +goose Down
ALTER TABLE member DROP COLUMN handle;
