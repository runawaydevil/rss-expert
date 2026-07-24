-- +goose Up
create table draft (
    account_id  integer primary key references account (id) on delete cascade,
    title       text,
    markdown    text    not null,
    in_reply_to text,
    saved_at    integer not null
) strict;

-- +goose Down
drop table draft;
