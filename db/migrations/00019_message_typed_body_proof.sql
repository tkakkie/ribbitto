-- Normalized typed body: 4-byte big-endian byte length, then SHA-256.
-- Legacy rows remain NULL; their original typed input is not proven.
-- +goose Up
ALTER TABLE message ADD COLUMN typed_body_proof bytea
    CHECK (typed_body_proof IS NULL OR octet_length(typed_body_proof) = 36);
-- +goose Down
ALTER TABLE message DROP COLUMN typed_body_proof;
