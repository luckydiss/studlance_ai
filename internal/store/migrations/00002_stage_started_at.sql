-- +goose Up
ALTER TABLE jobs ADD COLUMN stage_started_at INTEGER;

-- +goose Down
ALTER TABLE jobs DROP COLUMN stage_started_at;
