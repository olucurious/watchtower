-- Retention prunes by age; these keep the batched deletes cheap.
create index events_received_at on events (received_at);
create index issues_last_seen on issues (last_seen);
create index ingest_dead_letter_failed_at on ingest_dead_letter (failed_at);
