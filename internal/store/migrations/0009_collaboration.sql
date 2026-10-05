-- Commit links: a release that is a commit SHA links to <repo_url>/commit/<sha>.
alter table projects add column repo_url text not null default '';

-- One owner per issue. Removing a user unassigns their issues.
alter table issues add column assignee_id bigint references users (id) on delete set null;
create index issues_assignee on issues (assignee_id) where assignee_id is not null;

-- The activity log also carries assignments, delivered alerts and comments.
-- user_id identifies a comment's author so only they (or an admin) can delete it.
alter table issue_activity drop constraint issue_activity_kind_check;
alter table issue_activity add constraint issue_activity_kind_check check (kind in
  ('first_seen', 'regressed', 'resolved', 'unresolved', 'muted', 'assigned', 'unassigned', 'alerted', 'comment'));
alter table issue_activity add column user_id bigint references users (id) on delete set null;
