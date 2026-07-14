-- +migrate Up
CREATE TABLE banlist_sources (
	user_id INTEGER NOT NULL,
	provider TEXT NOT NULL,
	feed_type TEXT NOT NULL,
	generation TEXT NOT NULL,
	last_seen_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP,
	PRIMARY KEY (user_id, provider, feed_type)
);

CREATE INDEX idx_banlist_sources_expiry
ON banlist_sources(expires_at);

CREATE INDEX idx_banlist_sources_provider_feed
ON banlist_sources(provider, feed_type);

INSERT INTO banlist_sources (user_id, provider, feed_type, generation, last_seen_at, expires_at)
SELECT user_id, 'legacy', 'legacy', 'migration', CURRENT_TIMESTAMP, datetime('now', '+48 hours')
FROM banlist;

-- +migrate Down
DROP INDEX IF EXISTS idx_banlist_sources_provider_feed;
DROP INDEX IF EXISTS idx_banlist_sources_expiry;
DROP TABLE IF EXISTS banlist_sources;
