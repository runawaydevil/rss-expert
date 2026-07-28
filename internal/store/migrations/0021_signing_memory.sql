-- +goose Up
create table remote_host (
    host       text primary key,
    signing    text not null,
    updated_at integer not null
) strict;

-- +goose Down
drop table remote_host;
