-- +goose Up
create table block (
    id         integer primary key,
    account_id integer references account (id) on delete cascade,
    kind       text    not null check (kind in ('domain', 'source', 'item', 'word')),
    value      text    not null,
    reason     text,
    created_by integer not null references account (id),
    created_at integer not null
) strict;

create unique index block_once on block (coalesce(account_id, 0), kind, value);
create index block_instance on block (kind, value) where account_id is null;

create table report (
    id         integer primary key,
    item_key   text    not null,
    reporter   integer references account (id) on delete set null,
    reason     text    not null,
    context    text,
    state      text    not null default 'open' check (state in ('open', 'upheld', 'dismissed')),
    decided_by integer references account (id),
    decided_at integer,
    note       text,
    created_at integer not null
) strict;

create index report_open on report (created_at desc) where state = 'open';
create index report_item on report (item_key);

create table audit (
    id         integer primary key,
    actor      integer references account (id) on delete set null,
    actor_role text    not null,
    action     text    not null,
    subject    text,
    detail     text,
    at         integer not null
) strict;

create index audit_recent on audit (at desc);
create index audit_actor on audit (actor, at desc);

-- +goose Down
drop table audit;
drop table report;
drop table block;
