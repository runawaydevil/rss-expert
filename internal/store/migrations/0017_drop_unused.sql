-- +goose Up
drop index if exists subscription_source;
drop table if exists subscription;
alter table media drop column duration_ms;

-- +goose Down
alter table media add column duration_ms integer;

create table subscription (
    account_id integer not null references account (id) on delete cascade,
    source_id  integer not null references source (id) on delete cascade,
    created_at integer not null,
    primary key (account_id, source_id)
) strict;

create index subscription_source on subscription (source_id);
