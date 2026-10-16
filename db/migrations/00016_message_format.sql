-- +goose Up
-- How a body is read (decision 39). Every existing row is plain_text, and a
-- stored row's format never changes (decision 18). The default keeps posting
-- and branching notices plain_text until posting names chat_v1 (#1203).
ALTER TABLE message ADD COLUMN format text NOT NULL DEFAULT 'plain_text'
  CONSTRAINT message_format_check CHECK (format IN ('plain_text', 'chat_v1'));

-- +goose Down
ALTER TABLE message DROP COLUMN format;
