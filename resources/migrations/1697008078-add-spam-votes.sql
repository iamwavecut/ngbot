-- +migrate Up
CREATE TABLE IF NOT EXISTS "spam_votes" (
    "case_id" BIGINT NOT NULL,
    "voter_id" BIGINT NOT NULL,
    "vote" BOOLEAN NOT NULL,
    "voted_at" DATETIME NOT NULL,
    PRIMARY KEY ("case_id", "voter_id"),
    FOREIGN KEY ("case_id") REFERENCES "spam_cases" ("id") ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS "idx_spam_votes_case" ON "spam_votes" ("case_id");

-- +migrate Down
DROP TABLE IF EXISTS "spam_votes";
