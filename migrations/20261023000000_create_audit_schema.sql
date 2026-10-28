-- +goose Up
CREATE SCHEMA audit;

-- +goose Down
DROP SCHEMA audit;
