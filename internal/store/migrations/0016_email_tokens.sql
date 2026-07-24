-- +goose Up
alter table account add column email_verified_at integer;

create table email_token (
    id         integer primary key,
    account_id integer references account (id) on delete cascade,
    email      text    not null,
    purpose    text    not null,
    token_hash blob    not null unique,
    expires_at integer not null,
    used_at    integer,
    created_at integer not null
) strict;

create index email_token_live on email_token (expires_at) where used_at is null;

-- +goose Down
drop table email_token;
alter table account drop column email_verified_at;
