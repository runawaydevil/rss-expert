-- +goose Up
create table site (
    id            integer primary key,
    account_id    integer not null references account (id) on delete cascade,
    url           text    not null,
    host          text    not null,
    name          text,
    photo         text,
    note          text,
    feed_url      text,
    verified_at   integer,
    checked_at    integer,
    last_error    text,
    created_at    integer not null
) strict;

create unique index site_host on site (host);
create index site_account on site (account_id, created_at);

-- +goose Down
drop table site;
