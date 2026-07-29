-- +goose Up
alter table account add column display_name text not null default '';
alter table account add column bio text not null default '';
alter table account add column avatar_sha text not null default '';
alter table account add column banner_sha text not null default '';

-- +goose Down
alter table account drop column banner_sha;
alter table account drop column avatar_sha;
alter table account drop column bio;
alter table account drop column display_name;
