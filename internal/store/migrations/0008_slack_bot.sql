-- Slack can also be reached with a bot token (chat.postMessage) instead of
-- an incoming webhook. The token is sealed in target_sealed like a webhook
-- URL; the channel ID is not secret and is stored as is.
alter table alert_channels drop constraint alert_channels_kind_check;
alter table alert_channels add constraint alert_channels_kind_check check (kind in ('slack', 'slack_bot'));
alter table alert_channels add column target_channel text not null default '';
