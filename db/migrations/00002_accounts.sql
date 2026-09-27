-- +goose Up
CREATE TABLE account (
  id            uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  email         text        NOT NULL UNIQUE CHECK (email = lower(email) AND octet_length(email) <= 254) CHECK (email = normalize(email, NFC)),
  display_name  text        NOT NULL CHECK (length(display_name) BETWEEN 1 AND 50) CHECK (display_name = normalize(display_name, NFC)),
  password_hash text        NOT NULL CHECK (starts_with(password_hash, '$argon2id$')),
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE session (
  id         uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  token_hash bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  account_id uuid        NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX session_account_id_idx ON session (account_id);
CREATE INDEX session_expires_at_idx ON session (expires_at);

CREATE TABLE member (
  id               uuid        NOT NULL PRIMARY KEY DEFAULT uuidv7(),
  organization_id  uuid        NOT NULL REFERENCES organization(id) ON DELETE RESTRICT,
  account_id       uuid        NOT NULL REFERENCES account(id) ON DELETE RESTRICT,
  role             text        NOT NULL CHECK (role IN ('owner', 'member')),
  joined_event_seq bigint      NOT NULL CHECK (joined_event_seq >= 1),
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, account_id),
  UNIQUE (organization_id, id)
);

-- +goose Down
DROP TABLE member;
DROP TABLE session;
DROP TABLE account;
