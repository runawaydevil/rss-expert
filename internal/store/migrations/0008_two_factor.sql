-- +goose Up
alter table account add column totp_secret text;
alter table account add column totp_confirmed_at integer;

alter table session add column reauth_at integer;

create table recovery_code (
    account_id integer not null references account (id) on delete cascade,
    code_hash  blob    not null,
    used_at    integer,
    created_at integer not null,
    primary key (account_id, code_hash)
) strict;

create index recovery_unused on recovery_code (account_id) where used_at is null;

-- +goose Down
drop table recovery_code;
alter table session drop column reauth_at;
alter table account drop column totp_confirmed_at;
alter table account drop column totp_secret;
