-- +goose Up
create table instance (
    key        text primary key,
    value      text not null,
    updated_at integer not null
) strict;

insert into instance (key, value, updated_at)
values ('schema_origin', 'rss-social', unixepoch());

-- +goose Down
drop table instance;
