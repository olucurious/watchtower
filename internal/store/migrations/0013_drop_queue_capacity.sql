-- Queue depth for load shedding is now estimated in the application. The
-- exact counter serialized every ingest commit on one row.
drop trigger ingest_queue_insert_depth on ingest_queue;
drop trigger ingest_queue_delete_depth on ingest_queue;
drop trigger ingest_queue_truncate_depth on ingest_queue;
drop function adjust_ingest_queue_depth();
drop function reset_ingest_queue_depth();
drop table ingest_queue_capacity;
