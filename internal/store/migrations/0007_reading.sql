-- +goose Up
create table read_state (
    account_id integer not null references account (id) on delete cascade,
    item_key   text    not null,
    read_at    integer,
    saved_at   integer,
    primary key (account_id, item_key)
) strict;

create index read_state_unread on read_state (account_id) where read_at is null;
create index read_state_saved on read_state (account_id, saved_at desc) where saved_at is not null;

create table collection (
    id         integer primary key,
    account_id integer not null references account (id) on delete cascade,
    name       text    not null,
    created_at integer not null
) strict;

create unique index collection_name on collection (account_id, name);

create table collection_source (
    collection_id integer not null references collection (id) on delete cascade,
    source_id     integer not null references source (id) on delete cascade,
    primary key (collection_id, source_id)
) strict;

create index collection_source_by_source on collection_source (source_id);

create virtual table item_search using fts5 (
    item_key unindexed,
    title,
    body,
    author,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- +goose Down
drop table item_search;
drop table collection_source;
drop table collection;
drop table read_state;
