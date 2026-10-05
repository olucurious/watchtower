-- A one-time code that lets the first administrator be created in the
-- browser. Only its digest is stored, and it is deleted once that account
-- exists.
create table setup_code (
  singleton   boolean primary key default true check (singleton),
  code_sha256 bytea not null,
  created_at  timestamptz not null default now()
);
