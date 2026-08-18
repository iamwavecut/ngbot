-- +migrate Up
ALTER TABLE chats
ADD COLUMN llm_moderation_profile TEXT NOT NULL DEFAULT 'general'
CHECK (llm_moderation_profile IN ('general', 'jobs_hr'));

ALTER TABLE chat_spam_examples
ADD COLUMN classification INTEGER NOT NULL DEFAULT 1
CHECK (classification IN (0, 1));

-- +migrate Down
ALTER TABLE chat_spam_examples DROP COLUMN classification;
ALTER TABLE chats DROP COLUMN llm_moderation_profile;
