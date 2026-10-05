// Typed client for Watchtower's /api/v1 endpoints. Types mirror the Go
// structs in internal/store and internal/event.

export type Level = "debug" | "info" | "warning" | "error" | "fatal"
export type IssueStatus = "unresolved" | "resolved" | "muted"

export interface User {
  id: number
  email: string
  name: string
  is_admin: boolean
  created_at: string
  disabled: boolean
}

export interface Project {
  id: number
  slug: string
  name: string
  created_at: string
  unresolved_issues: number
  events_24h: number
  repo_url: string
}

export interface AccessToken {
  id: number
  name: string
  scope: "read" | "write"
  hint: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
  revoked_at: string | null
}

export interface EmailPrefs {
  assigned: boolean
  regressed: boolean
  comments: boolean
  digest: "off" | "daily" | "weekly"
}

export interface Member {
  id: number
  name: string
  email: string
}

export interface Key {
  id: number
  adapter: string
  label: string
  created_at: string
  created_by: string
  revoked_at: string | null
}

export interface AdapterInfo {
  name: string
  summary: string
  routes: string[]
  tested_sdk: string[]
  experimental: boolean
  enabled: boolean
}

export type AlertKind = "slack" | "slack_bot" | "linear"

export interface AlertChannel {
  id: number
  kind: AlertKind
  target_channel: string
  target_label: string
  name: string
  target_hint: string
  on_new_issue: boolean
  on_regression: boolean
  frequency_threshold: number | null
  min_level: Level
  environment: string
  created_by: string
  created_at: string
  last_delivery_at: string | null
  last_error: string | null
}

export interface AlertInput {
  name: string
  kind?: AlertKind
  webhook_url?: string
  bot_token?: string
  channel?: string
  api_key?: string
  team_id?: string
  on_new_issue: boolean
  on_regression: boolean
  frequency_threshold: number | null
  min_level: Level
  environment: string
}

export interface UploadToken {
  id: number
  name: string
  project: string
  created_by: string
  created_at: string
  last_used_at: string | null
  revoked_at: string | null
}

export interface ArtifactBundle {
  id: number
  bundle_id: string
  release: string
  dist: string
  file_count: number
  size_bytes: number
  created_at: string
}

export interface IssueRow {
  id: number
  project: string
  title: string
  culprit: string
  level: Level
  status: IssueStatus
  times_seen: number
  first_seen: string
  last_seen: string
  first_release: string
  last_release: string
  regressed_at: string | null
  environments: string[]
  assignee_id: number | null
  assignee: string
  trend: number[] | null
}

export interface IssuePage {
  issues: IssueRow[]
  total: number
  counts: Partial<Record<IssueStatus, number>>
  trend: { bucket: "hour" | "day"; start: string; size: number }
}

export interface Series {
  start: string
  bucket: "hour" | "day"
  counts: number[]
}

export interface TagSummary {
  key: string
  total: number
  values: { value: string; count: number }[]
}

export interface Activity {
  id: number
  user_id: number | null
  kind: "first_seen" | "regressed" | "resolved" | "unresolved" | "muted" | "assigned" | "unassigned" | "alerted" | "comment" | "linked"
  actor: string
  at: string
  detail: Record<string, string> | null
}

export interface IssueDetail extends IssueRow {
  project_name: string
  resolved_at: string | null
  events_24h: number
  events_30d: number
  hourly: Series
  daily: Series
  tags: TagSummary[] | null
  activity: Activity[] | null
  links: IssueLink[] | null
  trackers: { id: number; name: string; team: string; kind: "linear" }[] | null
}

export interface IssueLink {
  provider: "linear"
  channel_id: number | null
  identifier: string
  url: string
  state: string
  created_at: string
}

export interface LinearTeam {
  id: string
  name: string
  key: string
}

export interface Frame {
  function?: string
  module?: string
  filename?: string
  abs_path?: string
  lineno?: number
  colno?: number
  in_app: boolean
  context_line?: string
  pre_context?: string[]
  post_context?: string[]
  minified?: { filename?: string; function?: string; lineno?: number; colno?: number }
}

export interface ExceptionValue {
  type?: string
  value?: string
  module?: string
  mechanism?: { type?: string; handled?: boolean; synthetic?: boolean }
  frames?: Frame[]
}

export interface EventData {
  id: string
  adapter: string
  timestamp: string
  received_at: string
  platform?: string
  level: Level
  message?: string
  logger?: string
  transaction?: string
  exceptions?: ExceptionValue[]
  release?: string
  environment?: string
  server_name?: string
  tags?: Record<string, string>
  context?: Record<string, string>
  fingerprint?: string[]
  trace?: { trace_id?: string; span_id?: string }
  user?: { id?: string }
  request?: { method?: string; url?: string }
  sdk: { name?: string; version?: string }
}

export interface EventDetail {
  event_id: string
  issue_id: number
  occurred_at: string
  received_at: string
  data: EventData
  older: string
  newer: string
  oldest: string
  latest: string
}

export interface EventRow {
  event_id: string
  occurred_at: string
  message: string
  release: string
  environment: string
  server_name: string
  user_id: string
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, data.error ?? res.statusText)
  return data as T
}

const get = <T>(path: string) => request<T>("GET", path)

function query(params: Record<string, string | number | undefined>) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") q.set(k, String(v))
  const s = q.toString()
  return s ? `?${s}` : ""
}

export interface IssueQuery {
  project?: string
  status?: string
  query?: string
  environment?: string
  period?: string
  sort?: string
  assignee?: string
  limit?: number
  offset?: number
}

export const api = {
  meta: () => get<{ version: string; has_users: boolean; retention_days: number; email_enabled: boolean }>("/meta"),
  me: () => get<User>("/auth/me"),
  login: (email: string, password: string) => request<User>("POST", "/auth/login", { email, password }),
  setup: (body: { code: string; name: string; email: string; password: string }) => request<User>("POST", "/setup", body),
  logout: () => request<void>("POST", "/auth/logout", {}),
  changePassword: (current_password: string, new_password: string) =>
    request<void>("POST", "/account/password", { current_password, new_password }),
  emailPrefs: () => get<EmailPrefs>("/account/notifications"),
  setEmailPrefs: (p: EmailPrefs) => request<void>("PUT", "/account/notifications", p),
  sendTestEmail: () => request<{ sent_to: string }>("POST", "/account/test-email", {}),
  accessTokens: () => get<{ tokens: AccessToken[]; mcp_url: string }>("/account/tokens"),
  createAccessToken: (t: { name: string; scope: "read" | "write"; expires_in_days: number }) =>
    request<{ token: string; access_token: AccessToken; mcp_url: string }>("POST", "/account/tokens", t),
  revokeAccessToken: (id: number) => request<void>("DELETE", `/account/tokens/${id}`),

  adapters: () => get<{ adapters: AdapterInfo[] }>("/adapters"),
  projects: () => get<{ projects: Project[] }>("/projects"),
  createProject: (slug: string, name: string) => request<Project>("POST", "/projects", { slug, name }),
  updateProject: (slug: string, repo_url: string) => request<void>("PATCH", `/projects/${slug}`, { repo_url }),
  keys: (slug: string) => get<{ keys: Key[]; public_url: string }>(`/projects/${slug}/keys`),
  createKey: (slug: string, adapter: string, label: string) =>
    request<{ key: string; settings: Record<string, string> }>("POST", `/projects/${slug}/keys`, { adapter, label }),
  revokeKey: (slug: string, id: number) => request<void>("DELETE", `/projects/${slug}/keys/${id}`),
  alerts: (slug: string) => get<{ channels: AlertChannel[]; secret_key_configured: boolean }>(`/projects/${slug}/alerts`),
  createAlert: (slug: string, a: AlertInput) => request<{ id: number }>("POST", `/projects/${slug}/alerts`, a),
  updateAlert: (slug: string, id: number, a: AlertInput) => request<void>("PUT", `/projects/${slug}/alerts/${id}`, a),
  deleteAlert: (slug: string, id: number) => request<void>("DELETE", `/projects/${slug}/alerts/${id}`),
  testAlert: (slug: string, id: number) => request<void>("POST", `/projects/${slug}/alerts/${id}/test`, {}),
  linearTeams: (slug: string, auth: { api_key?: string; channel_id?: number }) =>
    request<{ teams: LinearTeam[] }>("POST", `/projects/${slug}/linear/teams`, auth),
  createLinearIssue: (id: number, channel_id: number) => request<IssueLink>("POST", `/issues/${id}/linear`, { channel_id }),
  sourceMaps: (slug: string) => get<{ bundles: ArtifactBundle[]; tokens: UploadToken[]; public_url: string }>(`/projects/${slug}/sourcemaps`),
  createUploadToken: (slug: string, name: string) =>
    request<{ token: string; settings: Record<string, string> }>("POST", `/projects/${slug}/upload-tokens`, { name }),
  revokeUploadToken: (slug: string, id: number) => request<void>("DELETE", `/projects/${slug}/upload-tokens/${id}`),
  deleteBundle: (slug: string, id: number) => request<void>("DELETE", `/projects/${slug}/sourcemaps/${id}`),
  environments: (project?: string) => get<{ environments: string[] }>(`/environments${query({ project })}`),

  issues: (q: IssueQuery) => get<IssuePage>(`/issues${query({ ...q })}`),
  issue: (id: number) => get<IssueDetail>(`/issues/${id}`),
  issueEvents: (id: number, offset = 0) => get<{ events: EventRow[] }>(`/issues/${id}/events${query({ offset, limit: 50 })}`),
  issueEvent: (id: number, which: string) => get<EventDetail>(`/issues/${id}/events/${which}`),
  setStatus: (ids: number[], status: IssueStatus) =>
    request<{ updated: number }>("POST", "/issues/status", { ids, status }),
  setAssignee: (id: number, user_id: number | null) => request<void>("PUT", `/issues/${id}/assignee`, { user_id }),
  addComment: (id: number, body: string) => request<{ id: number }>("POST", `/issues/${id}/comments`, { body }),
  deleteComment: (id: number, commentId: number) => request<void>("DELETE", `/issues/${id}/comments/${commentId}`),
  members: () => get<{ members: Member[] }>("/members"),

  users: () => get<{ users: User[] }>("/users"),
  createUser: (u: { email: string; name: string; password: string; is_admin: boolean }) =>
    request<User>("POST", "/users", u),
  setUserDisabled: (id: number, disabled: boolean) => request<void>("PATCH", `/users/${id}`, { disabled }),
}
