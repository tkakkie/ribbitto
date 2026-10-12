-- +goose Up
ALTER TABLE channel ADD COLUMN image text;

-- +goose Down
ALTER TABLE channel DROP COLUMN image;
