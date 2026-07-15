-- +migrate Up
CREATE TABLE banlist_generations (
	id INTEGER PRIMARY KEY,
	provider TEXT NOT NULL,
	feed_type TEXT NOT NULL,
	generation TEXT NOT NULL,
	last_seen_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP,
	active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
	UNIQUE (provider, feed_type, generation)
);

CREATE INDEX idx_banlist_generations_active_expiry
ON banlist_generations(active, expires_at);

CREATE INDEX idx_banlist_generations_provider_feed_active
ON banlist_generations(provider, feed_type, active);

CREATE TABLE banlist_entries (
	generation_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	PRIMARY KEY (generation_id, user_id),
	FOREIGN KEY (generation_id) REFERENCES banlist_generations(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_banlist_entries_user
ON banlist_entries(user_id, generation_id);

INSERT INTO banlist_generations (
	provider, feed_type, generation, last_seen_at, expires_at, active
)
SELECT
	provider,
	feed_type,
	generation,
	MAX(last_seen_at),
	MAX(expires_at),
	1
FROM banlist_sources
WHERE provider != 'legacy'
	AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
GROUP BY provider, feed_type, generation;

INSERT INTO banlist_entries (generation_id, user_id)
SELECT generation.id, source.user_id
FROM banlist_sources AS source
JOIN banlist_generations AS generation
	ON generation.provider = source.provider
	AND generation.feed_type = source.feed_type
	AND generation.generation = source.generation
WHERE source.provider != 'legacy'
	AND (source.expires_at IS NULL OR source.expires_at > CURRENT_TIMESTAMP);

DELETE FROM banlist;

INSERT INTO banlist (user_id)
SELECT DISTINCT entry.user_id
FROM banlist_entries AS entry
JOIN banlist_generations AS generation ON generation.id = entry.generation_id
WHERE generation.active = 1;

DROP INDEX IF EXISTS idx_banlist_sources_provider_feed;
DROP INDEX IF EXISTS idx_banlist_sources_expiry;
DROP TABLE banlist_sources;

-- +migrate Down
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

INSERT INTO banlist_sources (
	user_id, provider, feed_type, generation, last_seen_at, expires_at
)
SELECT
	entry.user_id,
	generation.provider,
	generation.feed_type,
	generation.generation,
	generation.last_seen_at,
	generation.expires_at
FROM banlist_entries AS entry
JOIN banlist_generations AS generation ON generation.id = entry.generation_id
WHERE generation.active = 1
ORDER BY generation.last_seen_at
ON CONFLICT(user_id, provider, feed_type) DO UPDATE SET
	generation = excluded.generation,
	last_seen_at = excluded.last_seen_at,
	expires_at = excluded.expires_at;

DROP INDEX IF EXISTS idx_banlist_entries_user;
DROP TABLE banlist_entries;
DROP INDEX IF EXISTS idx_banlist_generations_provider_feed_active;
DROP INDEX IF EXISTS idx_banlist_generations_active_expiry;
DROP TABLE banlist_generations;
