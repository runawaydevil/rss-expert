-- +goose Up
alter table source add column is_local integer not null default 0;

alter table account add column handle text;
create unique index account_handle on account (handle) where handle is not null;

create table post (
    id           integer primary key,
    account_id   integer not null references account (id) on delete cascade,
    guid         text    not null unique,
    title        text,
    markdown     text    not null,
    html         text    not null,
    in_reply_to  text,
    published_at integer not null,
    updated_at   integer,
    deleted_at   integer
) strict;

create index post_account on post (account_id, published_at desc);
create index post_thread on post (in_reply_to) where in_reply_to is not null;
create index post_recent on post (published_at desc) where deleted_at is null;

-- +goose Down
drop table post;
drop index account_handle;
alter table account drop column handle;
alter table source drop column is_local;
