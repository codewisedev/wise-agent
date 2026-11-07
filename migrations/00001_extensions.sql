-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS vector;

-- +goose Down
-- Extensions are intentionally left installed; dropping them could break later data.
SELECT 1;
