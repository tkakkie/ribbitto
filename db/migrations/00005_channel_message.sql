-- +goose Up
CREATE TABLE channel (
  id              uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid        NOT NULL REFERENCES organization(id) ON DELETE RESTRICT,
  name            text        NOT NULL CHECK (length(name) BETWEEN 1 AND 80) CHECK (name = normalize(name, NFC)),
  is_default      boolean     NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, name),
  UNIQUE (organization_id, id)
);
CREATE UNIQUE INDEX channel_default_idx ON channel (organization_id) WHERE is_default;

CREATE TABLE message (
  id              uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  organization_id uuid        NOT NULL,
  channel_id      uuid        NOT NULL,
  member_id       uuid        NOT NULL,
  body            text        NOT NULL CHECK (length(body) BETWEEN 1 AND 4000)
    -- PostgreSQL text already rejects NUL. Use explicit ranges, independent of locale.
    -- The bidi embeddings and overrides LRE, RLE, PDF, LRO and RLO are listed
    -- one by one; the isolates (U+2066–U+2069) stay allowed (docs/messages.md).
    CHECK (body !~ U&'[\0001-\0008\000B-\001F\007F-\009F\2028\2029\202A\202B\202C\202D\202E]')
    CHECK (body = btrim(body, U&'\0009\000A\000B\000C\000D\0020')),
  event_seq       bigint      NOT NULL CHECK (event_seq >= 1),
  created_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (organization_id, channel_id) REFERENCES channel (organization_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (organization_id, member_id) REFERENCES member (organization_id, id) ON DELETE RESTRICT,
  UNIQUE (organization_id, event_seq)
);
CREATE INDEX message_channel_event_seq_idx ON message (organization_id, channel_id, event_seq);

-- +goose Down
DROP TABLE message;
DROP TABLE channel;
