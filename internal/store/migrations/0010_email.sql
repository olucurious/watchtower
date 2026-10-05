-- Personal email preferences. Notifications are on by default for things
-- that concern you directly; the digest is opt-in.
alter table users add column email_assigned  boolean not null default true;
alter table users add column email_regressed boolean not null default true;
alter table users add column email_comments  boolean not null default true;
alter table users add column email_digest    text not null default 'off' check (email_digest in ('off', 'daily', 'weekly'));

-- Outbox of personal emails, written in the same transaction as the action
-- that caused them and delivered by the email worker. dedupe_key makes a
-- scheduled digest idempotent across workers.
create table emails (
  id           bigserial primary key,
  user_id      bigint not null references users (id) on delete cascade,
  kind         text not null check (kind in ('assigned', 'regressed', 'commented', 'digest')),
  issue_id     bigint references issues (id) on delete cascade,
  detail       jsonb not null default '{}',
  dedupe_key   text unique,
  created_at   timestamptz not null default now(),
  available_at timestamptz not null default now(),
  attempts     int not null default 0,
  sent_at      timestamptz,
  last_error   text
);
create index emails_pending on emails (available_at, id) where sent_at is null;
create index emails_created on emails (created_at);
