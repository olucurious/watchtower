-- Where alerts go and what triggers them. The Slack webhook URL is a
-- credential: it is stored sealed (AES-256-GCM, WATCHTOWER_SECRET_KEY) and
-- only a short hint is ever shown back.
create table alert_channels (
  id                  bigserial primary key,
  project_id          bigint not null references projects (id) on delete cascade,
  kind                text not null check (kind in ('slack')),
  name                text not null,
  target_sealed       bytea not null,
  target_hint         text not null,
  on_new_issue        boolean not null default true,
  on_regression       boolean not null default true,
  frequency_threshold int check (frequency_threshold > 0), -- events per hour; null disables
  min_level           text not null default 'error' check (min_level in ('debug', 'info', 'warning', 'error', 'fatal')),
  environment         text not null default '', -- '' matches every environment
  created_by          bigint references users (id) on delete set null,
  created_at          timestamptz not null default now(),
  last_delivery_at    timestamptz,
  last_error          text
);
create index alert_channels_project on alert_channels (project_id);

-- Transactional outbox: rows are written in the same transaction as the
-- event that triggered them, then delivered by the notifier.
create table notifications (
  id           bigserial primary key,
  channel_id   bigint not null references alert_channels (id) on delete cascade,
  issue_id     bigint references issues (id) on delete cascade, -- null for test messages
  kind         text not null check (kind in ('new_issue', 'regression', 'frequency', 'test')),
  detail       jsonb not null default '{}',
  created_at   timestamptz not null default now(),
  available_at timestamptz not null default now(),
  attempts     int not null default 0,
  sent_at      timestamptz,
  last_error   text
);
create index notifications_pending on notifications (available_at, id) where sent_at is null;
create index notifications_frequency on notifications (channel_id, issue_id, created_at) where kind = 'frequency';
create index notifications_created on notifications (created_at);
