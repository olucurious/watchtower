-- Environments an issue has occurred in, maintained by the worker, so the
-- issue list can filter by environment without scanning events.
alter table issues add column environments text[] not null default '{}';
create index issues_environments on issues using gin (environments);

update issues i set environments = coalesce((
  select array_agg(distinct e.data->>'environment')
  from events e where e.issue_id = i.id and coalesce(e.data->>'environment', '') <> ''
), '{}');
