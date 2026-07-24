-- +goose Up
alter table account add column ap_private_key text;
alter table account add column ap_public_key text;

create table follower (
    id           integer primary key,
    account_id   integer not null references account (id) on delete cascade,
    actor        text    not null,
    inbox        text    not null,
    shared_inbox text,
    created_at   integer not null
) strict;

create unique index follower_one on follower (account_id, actor);
create index follower_by_account on follower (account_id);

create table remote_actor (
    id             integer primary key,
    actor          text    not null unique,
    inbox          text    not null,
    shared_inbox   text,
    public_key_id  text    not null,
    public_key_pem text    not null,
    username       text,
    name           text,
    fetched_at     integer not null
) strict;

create table inbox_seen (
    activity_id text    primary key,
    seen_at     integer not null
) strict;

create index inbox_seen_age on inbox_seen (seen_at);

-- +goose Down
drop table inbox_seen;
drop table remote_actor;
drop index follower_by_account;
drop index follower_one;
drop table follower;
alter table account drop column ap_public_key;
alter table account drop column ap_private_key;
