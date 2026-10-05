-- Tokens for build tools (sentry-cli, bundler plugins) to upload source
-- maps. Stored as SHA-256 digests; shown once. project_id null = any project.
create table api_tokens (
  id           bigserial primary key,
  name         text not null,
  token_sha256 bytea not null unique,
  scope        text not null check (scope in ('sourcemaps')),
  project_id   bigint references projects (id) on delete cascade,
  created_by   bigint references users (id) on delete set null,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);

-- Chunks of an upload in progress, keyed by SHA-1 as in Sentry's protocol.
create table upload_chunks (
  checksum   text primary key,
  data       bytea not null,
  created_at timestamptz not null default now()
);

create table artifact_bundles (
  id          bigserial primary key,
  project_id  bigint not null references projects (id) on delete cascade,
  checksum    text not null,
  bundle_id   text not null default '',
  release     text not null default '',
  dist        text not null default '',
  file_count  int not null,
  size_bytes  bigint not null,
  created_at  timestamptz not null default now(),
  unique (project_id, checksum)
);
create index artifact_bundles_created on artifact_bundles (created_at);

-- Individual files, gzip-compressed. Minified sources and their maps share
-- a debug ID; url is the bundle's "~/path" form for release lookups.
create table artifact_files (
  id            bigserial primary key,
  bundle_id     bigint not null references artifact_bundles (id) on delete cascade,
  project_id    bigint not null,
  kind          text not null check (kind in ('minified_source', 'source_map', 'source')),
  url           text not null,
  debug_id      text not null default '',
  sourcemap_ref text not null default '',
  content_gzip  bytea not null
);
create index artifact_files_debug_id on artifact_files (project_id, debug_id, kind) where debug_id <> '';
create index artifact_files_url on artifact_files (project_id, url, kind);
