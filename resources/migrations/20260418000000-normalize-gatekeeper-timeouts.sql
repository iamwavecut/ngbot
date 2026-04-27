-- +migrate Up
UPDATE chats
SET challenge_timeout = challenge_timeout * 1000000000
WHERE challenge_timeout > 0 AND challenge_timeout < 1000000000;

UPDATE chats
SET reject_timeout = reject_timeout * 1000000000
WHERE reject_timeout > 0 AND reject_timeout < 1000000000;

-- +migrate Down
UPDATE chats
SET challenge_timeout = challenge_timeout / 1000000000
WHERE challenge_timeout >= 1000000000 AND challenge_timeout % 1000000000 = 0;

UPDATE chats
SET reject_timeout = reject_timeout / 1000000000
WHERE reject_timeout >= 1000000000 AND reject_timeout % 1000000000 = 0;
