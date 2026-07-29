-- +goose Up
alter table post add column visibility text not null default 'public';
create index post_visibility on post (visibility, published_at desc);

-- +goose Down
drop index post_visibility;
alter table post drop column visibility;
