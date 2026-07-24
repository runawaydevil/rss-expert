-- +goose Up
create table media (
    id            integer primary key,
    account_id    integer not null references account (id) on delete cascade,
    sha256        blob    not null,
    media_type    text    not null,
    byte_length   integer not null,
    stored_length integer not null,
    width         integer,
    height        integer,
    duration_ms   integer,
    alt           text,
    original_name text,
    stripped      integer not null default 0,
    created_at    integer not null
) strict;

create unique index media_dedupe on media (account_id, sha256);
create index media_recent on media (account_id, created_at desc);

create table post_media (
    post_id  integer not null references post (id) on delete cascade,
    media_id integer not null references media (id) on delete cascade,
    position integer not null,
    primary key (post_id, media_id)
) strict;

create index post_media_by_media on post_media (media_id);

-- +goose Down
drop table post_media;
drop table media;
