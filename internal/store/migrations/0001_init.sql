create table projects (
  id         bigserial primary key,
  slug       text not null unique check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  name       text not null,
  created_at timestamptz not null default now()
);

-- Keys are stored as SHA-256 digests; the plaintext is shown once at creation.
create table project_keys (
  id         bigserial primary key,
  project_id bigint not null references projects (id) on delete cascade,
  adapter    text not null,
  key_sha256 bytea not null,
  label      text not null default '',
  created_at timestamptz not null default now(),
  revoked_at timestamptz,
  unique (adapter, key_sha256)
);

-- Durable hand-off from adapters to workers. Events are scrubbed before
-- they are written here.
create table ingest_queue (
  id           bigserial primary key,
  project_id   bigint not null references projects (id) on delete cascade,
  event        jsonb not null,
  enqueued_at  timestamptz not null default now(),
  available_at timestamptz not null default now(),
  attempts     int not null default 0,
  last_error   text
);
create index ingest_queue_available on ingest_queue (available_at, id);

create table ingest_dead_letter (
  id          bigint primary key,
  project_id  bigint not null references projects (id) on delete cascade,
  event       jsonb not null,
  enqueued_at timestamptz not null,
  failed_at   timestamptz not null default now(),
  attempts    int not null,
  last_error  text not null
);

create table issues (
  id           bigserial primary key,
  project_id   bigint not null references projects (id) on delete cascade,
  fingerprint  text not null, -- "v<grouping version>:<hash>"
  title        text not null,
  culprit      text not null default '',
  level        text not null,
  status       text not null default 'unresolved' check (status in ('unresolved', 'resolved', 'muted')),
  first_seen   timestamptz not null,
  last_seen    timestamptz not null,
  times_seen   bigint not null default 0,
  first_release text not null default '',
  last_release text not null default '',
  resolved_at  timestamptz,
  regressed_at timestamptz,
  unique (project_id, fingerprint)
);
create index issues_project_status_last_seen on issues (project_id, status, last_seen desc);

-- Who did what to an issue, and when it reopened on its own.
create table issue_activity (
  id       bigserial primary key,
  issue_id bigint not null references issues (id) on delete cascade,
  kind     text not null check (kind in ('first_seen', 'regressed', 'resolved', 'unresolved', 'muted')),
  actor    text not null default 'system',
  at       timestamptz not null default now(),
  detail   jsonb not null default '{}'
);
create index issue_activity_issue on issue_activity (issue_id, at);

-- One row per accepted occurrence. (project_id, event_id) deduplicates
-- SDK retries without losing distinct occurrences.
create table events (
  project_id  bigint not null references projects (id) on delete cascade,
  event_id    text not null,
  issue_id    bigint references issues (id) on delete cascade,
  occurred_at timestamptz not null,
  received_at timestamptz not null,
  data        jsonb not null,
  primary key (project_id, event_id)
);
create index events_issue_occurred on events (issue_id, occurred_at desc);
