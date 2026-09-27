-- +goose Up
CREATE TABLE setup (
  id              boolean     PRIMARY KEY DEFAULT true CHECK (id),
  organization_id uuid        NOT NULL REFERENCES organization(id) ON DELETE RESTRICT,
  completed_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE setup;
