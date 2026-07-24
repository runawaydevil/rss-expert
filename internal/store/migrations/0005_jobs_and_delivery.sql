-- +goose Up
create table job (
    id           integer primary key,
    kind         text    not null,
    payload      text    not null,
    run_after    integer not null,
    lease_until  integer,
    attempts     integer not null default 0,
    max_attempts integer not null default 5,
    idem_key     text,
    last_error   text,
    created_at   integer not null,
    done_at      integer,
    dead_at      integer
) strict;

create unique index job_idem on job (idem_key) where idem_key is not null and done_at is null and dead_at is null;
create index job_ready on job (run_after) where done_at is null and dead_at is null;
create index job_dead on job (dead_at) where dead_at is not null;

create table delivery_attempt (
    id           integer primary key,
    item_key     text    not null,
    target       text    not null,
    protocol     text    not null,
    attempt_no   integer not null,
    http_status  integer,
    latency_ms   integer,
    outcome      text    not null,
    error_kind   text,
    error_detail text,
    at           integer not null
) strict;

create index delivery_item on delivery_attempt (item_key, at desc);
create index delivery_recent on delivery_attempt (at desc);

-- +goose Down
drop table delivery_attempt;
drop table job;
