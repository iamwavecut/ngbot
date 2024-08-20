-- +migrate Up
drop table if exists "charade_scores";
drop table if exists "meta";
drop table if exists "users";

alter table "chats"
drop column "title"
drop column "type"
add column "enabled" boolean not null default true
add column "migrated" boolean not null default false
add column "challenge_timeout" interval not null default '3 minutes'
add column "reject_timeout" interval not null default '10 minutes'
;

create table chat_members (
    chat_id bigint not null references chats(id) on delete cascade,
    user_id bigint not null references users(id) on delete cascade,
    primary key (chat_id, user_id)
);

-- +migrate Down
-- nothing to do
