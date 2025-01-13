-- +migrate Up
CREATE TABLE IF NOT EXISTS kv_store (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_kv_store_updated_at ON kv_store(updated_at);

-- +migrate Down
DROP TABLE IF EXISTS kv_store; 
