-- +goose Up
-- A message's quote of an earlier message (decision 38, #1176): the original's
-- ID, its author and a snapshot of its text under the body's checks. Author
-- and snapshot are set together; the ID only with them, and deleting the
-- original nulls only the ID, so the quote keeps its author and text.
ALTER TABLE message ADD CONSTRAINT message_organization_id_id_key UNIQUE (organization_id, id);
ALTER TABLE message
  ADD COLUMN quoted_message_id uuid,
  ADD COLUMN quoted_member_id uuid,
  ADD COLUMN quote_text text
    CONSTRAINT message_quote_text_length_check CHECK (length(quote_text) BETWEEN 1 AND 4000)
    CONSTRAINT message_quote_text_characters_check
      CHECK (quote_text !~ U&'[\0001-\0008\000B-\001F\007F-\009F\2028\2029\202A\202B\202C\202D\202E]')
    CONSTRAINT message_quote_text_trim_check
      CHECK (quote_text = btrim(quote_text, U&'\0009\000A\000B\000C\000D\0020')),
  ADD CONSTRAINT message_quote_check CHECK (
    (quoted_member_id IS NULL) = (quote_text IS NULL)
    AND (quoted_message_id IS NULL OR quoted_member_id IS NOT NULL)),
  ADD CONSTRAINT message_quoted_message_fkey
    FOREIGN KEY (organization_id, quoted_message_id)
    REFERENCES message (organization_id, id) ON DELETE SET NULL (quoted_message_id),
  ADD CONSTRAINT message_quoted_member_fkey
    FOREIGN KEY (organization_id, quoted_member_id)
    REFERENCES member (organization_id, id) ON DELETE RESTRICT;
-- Deleting an original finds its quotes through this index, not a scan.
CREATE INDEX message_quoted_message_idx ON message (organization_id, quoted_message_id)
  WHERE quoted_message_id IS NOT NULL;

-- +goose Down
DROP INDEX message_quoted_message_idx;
ALTER TABLE message DROP COLUMN quote_text, DROP COLUMN quoted_member_id, DROP COLUMN quoted_message_id;
ALTER TABLE message DROP CONSTRAINT message_organization_id_id_key;
