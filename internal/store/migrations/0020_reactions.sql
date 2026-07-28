-- +goose Up
create table reaction (
    id          integer primary key,
    item_key    text    not null,
    actor       text    not null,
    kind        text    not null,
    activity_id text    not null,
    created_at  integer not null
) strict;

create unique index reaction_one on reaction (item_key, actor, kind);
create index reaction_by_item on reaction (item_key, kind);

-- +goose Down
drop index reaction_by_item;
drop index reaction_one;
drop table reaction;
