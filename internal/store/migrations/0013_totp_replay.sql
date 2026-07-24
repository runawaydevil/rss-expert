-- +goose Up
alter table account add column totp_last_step integer;

-- +goose Down
alter table account drop column totp_last_step;
