-- +goose Up
create table source (
    id               integer primary key,
    feed_url         text    not null unique,
    site_url         text,
    title            text,
    self_url         text,
    etag             text,
    last_modified    text,
    last_fetch_at    integer,
    last_status      integer,
    last_error       text,
    failure_count    integer not null default 0,
    poll_interval    integer not null default 900,
    next_poll_at     integer not null default 0,
    push_hub         text,
    push_verified_at integer,
    quarantined_at   integer,
    created_at       integer not null
) strict;

create index source_next_poll on source (next_poll_at);

create table subscription (
    account_id integer not null references account (id) on delete cascade,
    source_id  integer not null references source (id) on delete cascade,
    created_at integer not null,
    primary key (account_id, source_id)
) strict;

create index subscription_source on subscription (source_id);

create table raw_payload (
    sha256      blob    primary key,
    body        blob    not null,
    byte_length integer not null,
    media_type  text,
    first_seen  integer not null,
    keep_until  integer
) strict;

create table observation (
    id                integer primary key,
    source_id         integer not null references source (id) on delete cascade,
    payload_sha256    blob    not null references raw_payload (sha256),
    item_key          text    not null,
    observed_at       integer not null,
    published_at      integer,
    updated_at        integer,
    title             text,
    html              text,
    markdown          text,
    author            text,
    link              text,
    guid_is_permalink integer not null default 1,
    in_reply_to       text,
    comments_url      text,
    comments_count    integer,
    origin_name       text,
    origin_url        text,
    passthrough       text,
    fidelity          integer not null,
    claimed_by_author integer not null,
    content_hash      blob    not null
) strict;

create index observation_key on observation (item_key, observed_at desc);
create index observation_source on observation (source_id);
create unique index observation_once on observation (source_id, item_key, content_hash);

create table logical_item (
    item_key     text    primary key,
    winner_id    integer not null references observation (id),
    reason       text    not null,
    published_at integer,
    updated_at   integer,
    in_reply_to  text,
    converged_at integer not null
) strict;

create index logical_item_timeline on logical_item (published_at desc);
create index logical_item_thread on logical_item (in_reply_to) where in_reply_to is not null;

-- +goose Down
drop table logical_item;
drop table observation;
drop table raw_payload;
drop table subscription;
drop table source;
