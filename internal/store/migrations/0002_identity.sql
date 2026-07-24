-- +goose Up
create table account (
    id            integer primary key,
    email         text    not null,
    email_folded  text    not null unique,
    password_hash text    not null,
    role          text    not null check (role in ('owner', 'admin', 'moderator', 'user')),
    created_at    integer not null,
    disabled_at   integer
) strict;

create unique index account_single_owner on account (role) where role = 'owner';

create table session (
    token_hash blob    primary key,
    account_id integer not null references account (id) on delete cascade,
    created_at integer not null,
    expires_at integer not null,
    last_seen  integer not null
) strict;

create index session_account on session (account_id);
create index session_expiry on session (expires_at);

-- +goose Down
drop table session;
drop index account_single_owner;
drop table account;
