-- +goose Up
alter table source add column protocol text not null default 'feed';

create index source_due on source (protocol, is_local, next_poll_at);

-- +goose Down
drop index source_due;
alter table source drop column protocol;
