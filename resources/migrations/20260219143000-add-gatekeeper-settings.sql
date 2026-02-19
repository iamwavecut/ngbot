-- +migrate Up
ALTER TABLE chats ADD COLUMN gatekeeper_captcha_enabled BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN gatekeeper_greeting_enabled BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN gatekeeper_captcha_options_count INTEGER NOT NULL DEFAULT 5;
ALTER TABLE chats ADD COLUMN gatekeeper_greeting_text TEXT NOT NULL DEFAULT '';

UPDATE chats
SET gatekeeper_captcha_enabled = COALESCE(gatekeeper_captcha_enabled, 0),
    gatekeeper_greeting_enabled = COALESCE(gatekeeper_greeting_enabled, 0),
    gatekeeper_captcha_options_count = CASE
        WHEN gatekeeper_captcha_options_count IN (3, 4, 5, 6, 8, 10) THEN gatekeeper_captcha_options_count
        ELSE 5
    END,
    gatekeeper_greeting_text = COALESCE(gatekeeper_greeting_text, '');

-- +migrate Down
SELECT 1;
