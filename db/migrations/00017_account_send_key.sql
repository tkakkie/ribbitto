-- +goose Up
-- Which key sends from the composer, chosen once per person and applied in
-- every organisation (#1175). The default keeps existing accounts on Enter.
ALTER TABLE account ADD COLUMN send_key text NOT NULL DEFAULT 'enter'
  CONSTRAINT account_send_key_check CHECK (send_key IN ('enter', 'modifier_enter'));

-- +goose Down
ALTER TABLE account DROP COLUMN send_key;
