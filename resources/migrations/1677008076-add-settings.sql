-- +migrate Up
create table if not exists "meta" (
    "id"   bigint not null,
    "lang" text   not null default 'en',
    primary key ("id")
);
-- no data migration is meant to be done

-- +migrate Down
drop table if exists "meta";
-- nothing to do
