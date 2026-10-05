-- Linear as an alert destination: the sealed target is an API key,
-- target_channel the team's ID and target_label its display name.
alter table alert_channels drop constraint alert_channels_kind_check;
alter table alert_channels add constraint alert_channels_kind_check check (kind in ('slack', 'slack_bot', 'linear'));
alter table alert_channels add column target_label text not null default '';

-- An issue tracked elsewhere. One link per provider per issue, so retried
-- or concurrent creation cannot produce a second tracker issue.
create table issue_links (
  issue_id    bigint not null references issues (id) on delete cascade,
  provider    text not null check (provider in ('linear')),
  channel_id  bigint references alert_channels (id) on delete set null,
  external_id text not null,
  identifier  text not null,
  url         text not null,
  state       text not null default '',
  created_at  timestamptz not null default now(),
  checked_at  timestamptz,
  primary key (issue_id, provider)
);
create index issue_links_channel on issue_links (channel_id);

alter table issue_activity drop constraint issue_activity_kind_check;
alter table issue_activity add constraint issue_activity_kind_check check (kind in
  ('first_seen', 'regressed', 'resolved', 'unresolved', 'muted', 'assigned', 'unassigned', 'alerted', 'comment', 'linked'));
