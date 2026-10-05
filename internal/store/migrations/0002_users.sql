create table users (
  id            bigserial primary key,
  email         text not null,
  name          text not null default '',
  password_hash text not null, -- argon2id, PHC string format
  is_admin      boolean not null default false,
  created_at    timestamptz not null default now(),
  disabled_at   timestamptz
);
create unique index users_email on users (lower(email));

-- Session tokens are stored as SHA-256 digests; the cookie holds the token.
create table sessions (
  token_sha256 bytea primary key,
  user_id      bigint not null references users (id) on delete cascade,
  created_at   timestamptz not null default now(),
  expires_at   timestamptz not null,
  last_seen_at timestamptz not null default now()
);
create index sessions_user on sessions (user_id);

-- Revoked keys stay listed for audit; this lets the UI show who made them.
alter table project_keys add column created_by bigint references users (id) on delete set null;
