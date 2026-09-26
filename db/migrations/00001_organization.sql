-- +goose Up
CREATE TABLE organization (
  id         uuid        PRIMARY KEY DEFAULT uuidv7(),
  slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
  name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  event_seq  bigint      NOT NULL DEFAULT 0 CHECK (event_seq >= 0),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE organization;
