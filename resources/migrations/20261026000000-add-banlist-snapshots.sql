-- +migrate Up
CREATE TABLE banlist_snapshots (
	source TEXT PRIMARY KEY,
	format INTEGER NOT NULL CHECK (format = 1),
	id_count INTEGER NOT NULL,
	payload BLOB NOT NULL,
	payload_sha256 BLOB NOT NULL,
	etag TEXT,
	last_modified TEXT,
	frozen INTEGER NOT NULL DEFAULT 0 CHECK (frozen IN (0, 1)),
	changed_at TIMESTAMP NOT NULL,
	checked_at TIMESTAMP NOT NULL
);

CREATE TABLE banlist_observations (
	user_id INTEGER NOT NULL,
	source TEXT NOT NULL,
	observed_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	PRIMARY KEY (user_id, source)
) WITHOUT ROWID;

CREATE INDEX idx_banlist_observations_expires
ON banlist_observations(expires_at);

INSERT INTO banlist_observations (user_id, source, observed_at, expires_at)
SELECT entry.user_id, generation.provider || '/' || generation.feed_type, MAX(generation.last_seen_at), MAX(generation.expires_at)
FROM banlist_entries AS entry
JOIN banlist_generations AS generation ON generation.id = entry.generation_id
WHERE generation.active = 1
	AND generation.feed_type IN ('online', 'hourly')
	AND generation.expires_at > CURRENT_TIMESTAMP
GROUP BY entry.user_id, generation.provider, generation.feed_type;

-- +migrate Down
DROP INDEX IF EXISTS idx_banlist_observations_expires;
DROP TABLE IF EXISTS banlist_observations;
DROP TABLE IF EXISTS banlist_snapshots;
