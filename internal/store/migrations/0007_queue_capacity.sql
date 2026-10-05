-- Keep queue accounting correct for COPY, worker deletes, project cascades,
-- and maintenance performed directly in SQL.
lock table ingest_queue in share row exclusive mode;
create table ingest_queue_capacity (
  singleton boolean primary key default true check (singleton),
  depth bigint not null check (depth >= 0)
);
insert into ingest_queue_capacity (depth) select count(*) from ingest_queue;

create function adjust_ingest_queue_depth() returns trigger language plpgsql as $$
begin
  if TG_OP = 'INSERT' then
    update ingest_queue_capacity set depth = depth + (select count(*) from new_queue_rows) where singleton;
  else
    update ingest_queue_capacity set depth = depth - (select count(*) from old_queue_rows) where singleton;
  end if;
  return null;
end;
$$;
create trigger ingest_queue_insert_depth after insert on ingest_queue
  referencing new table as new_queue_rows for each statement execute function adjust_ingest_queue_depth();
create trigger ingest_queue_delete_depth after delete on ingest_queue
  referencing old table as old_queue_rows for each statement execute function adjust_ingest_queue_depth();

create function reset_ingest_queue_depth() returns trigger language plpgsql as $$
begin
  update ingest_queue_capacity set depth = 0 where singleton;
  return null;
end;
$$;
create trigger ingest_queue_truncate_depth after truncate on ingest_queue
  for each statement execute function reset_ingest_queue_depth();
