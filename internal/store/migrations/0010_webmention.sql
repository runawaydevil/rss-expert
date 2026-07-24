-- +goose Up
create table webmention (
    id          integer primary key,
    direction   text    not null check (direction in ('in', 'out')),
    source      text    not null,
    target      text    not null,
    state       text    not null check (state in ('pending', 'verified', 'approved', 'rejected', 'failed', 'deleted')),
    author_name text,
    author_url  text,
    author_photo text,
    content     text,
    endpoint    text,
    last_error  text,
    received_at integer not null,
    verified_at integer,
    decided_at  integer,
    decided_by  integer references account (id) on delete set null
) strict;

create unique index webmention_once on webmention (direction, source, target);
create index webmention_target on webmention (target, state);
create index webmention_pending on webmention (received_at desc) where state = 'verified';

create table token (
    id          integer primary key,
    account_id  integer not null references account (id) on delete cascade,
    token_hash  blob    not null unique,
    client_id   text    not null,
    scope       text    not null,
    created_at  integer not null,
    last_used   integer,
    revoked_at  integer
) strict;

create index token_account on token (account_id) where revoked_at is null;

-- +goose Down
drop table token;
drop table webmention;
