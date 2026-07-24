-- +goose Up
alter table observation add column enclosures text;

-- +goose Down
alter table observation drop column enclosures;
