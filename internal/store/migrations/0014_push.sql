-- +goose Up
alter table source add column self_link text;
alter table source add column hub_url text;
alter table source add column hub_secret text;
alter table source add column hub_lease_until integer;
alter table source add column cloud_endpoint text;
alter table source add column cloud_until integer;
alter table source add column last_push_at integer;

create table push_intent (
    id         integer primary key,
    source_id  integer not null references source (id) on delete cascade,
    protocol   text    not null,
    topic      text    not null,
    mode       text    not null,
    secret     text,
    created_at integer not null
) strict;

create unique index push_intent_pending on push_intent (source_id, protocol);

create table hub_subscriber (
    id           integer primary key,
    topic        text    not null,
    callback     text    not null,
    secret       text,
    lease_until  integer not null,
    verified_at  integer,
    challenge    text,
    mode         text    not null,
    created_at   integer not null
) strict;

create unique index hub_subscriber_one on hub_subscriber (topic, callback);
create index hub_subscriber_live on hub_subscriber (topic) where verified_at is not null;

create table cloud_subscriber (
    id          integer primary key,
    topic       text    not null,
    callback    text    not null,
    lease_until integer not null,
    created_at  integer not null
) strict;

create unique index cloud_subscriber_one on cloud_subscriber (topic, callback);

-- +goose Down
drop table cloud_subscriber;
drop table hub_subscriber;
drop table push_intent;
alter table source drop column last_push_at;
alter table source drop column cloud_until;
alter table source drop column cloud_endpoint;
alter table source drop column hub_lease_until;
alter table source drop column hub_secret;
alter table source drop column hub_url;
alter table source drop column self_link;
