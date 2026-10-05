-- Personal access tokens for the MCP server and other API clients. A token
-- acts as its owner, so it stops working when the owner is disabled.
-- Stored as SHA-256 digests; the plaintext is shown once.
create table access_tokens (
  id           bigserial primary key,
  user_id      bigint not null references users (id) on delete cascade,
  name         text not null,
  hint         text not null, -- the first characters, so a token can be recognised
  token_sha256 bytea not null unique,
  scope        text not null check (scope in ('read', 'write')),
  created_at   timestamptz not null default now(),
  expires_at   timestamptz,
  last_used_at timestamptz,
  revoked_at   timestamptz
);
create index access_tokens_user on access_tokens (user_id);
