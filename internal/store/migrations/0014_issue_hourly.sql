-- Event counts per issue and UTC hour, kept by the worker in the same
-- transaction as the events, so charts, counts and digests read a few
-- rows instead of counting events. Existing events are counted here;
-- events stored by a not-yet-upgraded worker during a rolling upgrade
-- are not, which only affects charts until they age out.
create table issue_hourly (
  issue_id bigint not null references issues (id) on delete cascade,
  hour     timestamptz not null,
  events   bigint not null,
  primary key (issue_id, hour)
);
create index issue_hourly_hour on issue_hourly (hour);

insert into issue_hourly (issue_id, hour, events)
select issue_id, date_trunc('hour', occurred_at, 'UTC'), count(*)
from events where issue_id is not null group by 1, 2;
