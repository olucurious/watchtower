import { useState, type ReactNode } from "react"
import { Link, useNavigate, useParams } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { addDays, addHours, format } from "date-fns"
import { Bar, BarChart, XAxis } from "recharts"
import { toast } from "sonner"
import { ArrowUpRightIcon, BellOffIcon, CheckIcon, ChevronLeftIcon, ChevronRightIcon, RotateCcwIcon, SquareKanbanIcon, Trash2Icon, UserRoundIcon } from "lucide-react"
import { cn } from "cn"

import { api, type Activity, type EventDetail, type IssueDetail, type IssueStatus, type Series, type TagSummary } from "@/lib/api"
import { ago, count, dateTime, percent, shortRelease, splitTitle } from "@/lib/format"
import { CopyButton, LevelBadge, Release, StatusBadge, Time, UserAvatar, useProjectRepo } from "@/components/bits"
import { useMe } from "@/components/app-shell"
import { Exceptions } from "@/components/stacktrace"
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

export function IssuePage() {
  const { id, eventId } = useParams()
  const issueId = Number(id)
  const issue = useQuery({ queryKey: ["issue", issueId], queryFn: () => api.issue(issueId) })
  const event = useQuery({
    queryKey: ["issue-event", issueId, eventId ?? "latest"],
    queryFn: () => api.issueEvent(issueId, eventId ?? "latest"),
  })
  const [tab, setTab] = useState("details")
  const repo = useProjectRepo(issue.data?.project)

  if (issue.isPending) return <IssueSkeleton />
  if (issue.error) return <p className="text-sm text-muted-foreground">{issue.error.message}</p>
  const d = issue.data

  return (
    <div className="space-y-6">
      <IssueHeader issue={d} />
      <PhoneSummary issue={d} />
      <Tabs value={tab} onValueChange={(v) => setTab(String(v))}>
        <TabsList variant="line" className="-mb-px h-10 w-full justify-start border-b">
          <TabsTrigger value="details" className="flex-none px-3">
            Details
          </TabsTrigger>
          <TabsTrigger value="events" className="flex-none px-3">
            Events <span className="ml-1 text-xs text-muted-foreground tabular-nums">{count(d.times_seen)}</span>
          </TabsTrigger>
        </TabsList>
      </Tabs>
      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="min-w-0 space-y-8">
          {tab === "details" ? (
            <EventView issue={d} repo={repo} event={event.data} loading={event.isPending} error={event.error} />
          ) : (
            <EventList issueId={issueId} repo={repo} />
          )}
        </div>
        <IssueSidebar issue={d} repo={repo} />
      </div>
    </div>
  )
}

function IssueHeader({ issue }: { issue: IssueDetail }) {
  const { type, value } = splitTitle(issue.title)
  const qc = useQueryClient()
  const setStatus = useMutation({
    mutationFn: (status: IssueStatus) => api.setStatus([issue.id], status),
    onSuccess: (_, status) => {
      toast.success(status === "unresolved" ? "Issue reopened" : `Issue ${status}`)
      qc.invalidateQueries({ queryKey: ["issue", issue.id] })
      qc.invalidateQueries({ queryKey: ["issues"] })
    },
    onError: (e) => toast.error(e.message),
  })
  return (
    <div className="space-y-3">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink render={<Link to="/issues" />}>Issues</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink render={<Link to={`/issues?project=${issue.project}`} />}>{issue.project_name}</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage className="font-mono text-xs">WT-{issue.id}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1.5">
          <h1 className="text-xl leading-snug font-semibold tracking-tight break-words">
            {type}
            {value && <span className="font-normal text-muted-foreground">: {value}</span>}
          </h1>
          {issue.culprit && <p className="font-mono text-sm text-muted-foreground">{issue.culprit}</p>}
          <div className="flex flex-wrap items-center gap-3 pt-1">
            <StatusBadge status={issue.status} regressed={!!issue.regressed_at} />
            <LevelBadge level={issue.level} />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <LinearAction issue={issue} />
          <AssigneePicker issue={issue} />
          {issue.status === "unresolved" ? (
            <>
              <Button onClick={() => setStatus.mutate("resolved")} disabled={setStatus.isPending}>
                <CheckIcon /> Resolve
              </Button>
              <Button variant="outline" onClick={() => setStatus.mutate("muted")} disabled={setStatus.isPending}>
                <BellOffIcon /> Mute
              </Button>
            </>
          ) : (
            <Button variant="outline" onClick={() => setStatus.mutate("unresolved")} disabled={setStatus.isPending}>
              <RotateCcwIcon /> {issue.status === "resolved" ? "Unresolve" : "Unmute"}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}

function EventView({
  issue,
  repo,
  event,
  loading,
  error,
}: {
  issue: IssueDetail
  repo?: string
  event?: EventDetail
  loading: boolean
  error: Error | null
}) {
  if (error) return <p role="alert" className="text-sm text-destructive">{error.message}</p>
  if (loading || !event) return <Skeleton className="h-96 w-full" />
  const e = event.data
  return (
    <>
      <EventNav issueId={issue.id} event={event} />
      <Highlights event={event} repo={repo} />
      <Context values={e.context} />
      {e.exceptions && e.exceptions.length > 0 && !(e.exceptions.length === 1 && e.exceptions[0]!.mechanism?.synthetic && !e.exceptions[0]!.frames?.length) ? (
        <>
          {e.message && e.exceptions.every((x) => x.mechanism?.synthetic) && <MessageBlock message={e.message} />}
          <Exceptions values={e.exceptions} title={issue.title} />
        </>
      ) : (
        e.message && <MessageBlock message={e.message} />
      )}
      {e.tags && Object.keys(e.tags).length > 0 && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold">Tags</h2>
          <div className="flex flex-wrap gap-1.5">
            {Object.entries(e.tags)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => (
                <span key={k} className="inline-flex max-w-full overflow-hidden rounded-md border text-xs">
                  <span className="bg-muted px-2 py-1 text-muted-foreground">{k}</span>
                  <span className="truncate px-2 py-1 font-mono">{v}</span>
                </span>
              ))}
          </div>
        </section>
      )}
    </>
  )
}

/** Allowlisted diagnostics: where a log-reported error was logged, or which job failed. */
function Context({ values }: { values?: Record<string, string> }) {
  if (!values || Object.keys(values).length === 0) return null
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-semibold">Context</h2>
      <dl>
        {Object.entries(values)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([k, v]) => (
            <div key={k} className="flex min-w-0 items-baseline gap-3 border-b border-dashed py-1.5 text-sm">
              <dt className="w-40 shrink-0 font-mono text-xs break-all text-muted-foreground">{k}</dt>
              <dd className="min-w-0 font-mono break-all">{v}</dd>
            </div>
          ))}
      </dl>
    </section>
  )
}

function MessageBlock({ message }: { message: string }) {
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-semibold">Message</h2>
      <pre className="rounded-lg border bg-muted/40 p-3 text-sm break-words whitespace-pre-wrap">{message}</pre>
    </section>
  )
}

function EventNav({ issueId, event }: { issueId: number; event: EventDetail }) {
  const navigate = useNavigate()
  const go = (id: string) => navigate(`/issues/${issueId}/events/${id}`)
  const isLatest = !event.newer
  return (
    <div className="flex items-center justify-between gap-3 border-b pb-3">
      <div className="flex min-w-0 items-center gap-1.5 text-sm">
        <span className="truncate font-mono text-xs text-muted-foreground max-sm:max-w-28">{event.event_id}</span>
        <CopyButton value={event.event_id} label="Copy event ID" />
        <Time iso={event.occurred_at} className="whitespace-nowrap text-muted-foreground">
          {ago(event.occurred_at)}
        </Time>
        {isLatest && <span className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">Latest</span>}
      </div>
      <div className="flex shrink-0 items-center gap-1">
        {!isLatest && (
          <Button variant="ghost" size="sm" className="text-xs" onClick={() => go(event.latest)}>
            Latest
          </Button>
        )}
        <Button variant="outline" size="icon-sm" aria-label="Older event" disabled={!event.older} onClick={() => go(event.older)}>
          <ChevronLeftIcon />
        </Button>
        <Button variant="outline" size="icon-sm" aria-label="Newer event" disabled={!event.newer} onClick={() => go(event.newer)}>
          <ChevronRightIcon />
        </Button>
      </div>
    </div>
  )
}

function Highlights({ event, repo }: { event: EventDetail; repo?: string }) {
  const e = event.data
  const items: [string, ReactNode, string?][] = []
  if (e.environment) items.push(["Environment", e.environment])
  if (e.release) items.push(["Release", <Release key="release" release={e.release} repo={repo} />, e.release])
  if (e.transaction) items.push(["Transaction", <span key="transaction" className="font-mono">{e.transaction}</span>])
  if (e.request?.url) items.push(["Request", <span key="request" className="font-mono">{[e.request.method, e.request.url].filter(Boolean).join(" ")}</span>])
  if (e.server_name) items.push(["Server", e.server_name])
  if (e.user?.id) items.push(["User", <span key="user" className="font-mono">{e.user.id}</span>])
  if (e.trace?.trace_id) items.push(["Trace", <span key="trace" className="font-mono">{e.trace.trace_id}</span>, e.trace.trace_id])
  if (e.platform) items.push(["Platform", e.platform])
  if (e.sdk?.name) items.push(["SDK", `${e.sdk.name}${e.sdk.version ? ` ${e.sdk.version}` : ""}`])
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-semibold">Highlights</h2>
      <dl className="grid gap-x-8 gap-y-0 sm:grid-cols-2">
        {items.map(([label, value, copy]) => (
          <div key={label} className="group flex min-w-0 items-center gap-3 border-b border-dashed py-1.5 text-sm">
            <dt className="w-28 shrink-0 text-muted-foreground">{label}</dt>
            <dd className="flex min-w-0 items-center gap-1 truncate">
              <span className="truncate">{value}</span>
              {copy && <CopyButton value={copy} className="opacity-0 group-hover:opacity-100" />}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function EventList({ issueId, repo }: { issueId: number; repo?: string }) {
  const events = useQuery({ queryKey: ["issue-events", issueId], queryFn: () => api.issueEvents(issueId) })
  if (events.isPending) return <Skeleton className="h-64 w-full" />
  if (events.error) return <p role="alert" className="text-sm text-destructive">{events.error.message}</p>
  return (
    <div className="overflow-hidden rounded-lg border">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-left text-[11px] tracking-wide text-muted-foreground uppercase">
          <tr>
            <th className="px-3 py-2 font-medium">Event</th>
            <th className="px-3 py-2 font-medium">Time</th>
            <th className="px-3 py-2 font-medium max-md:hidden">Release</th>
            <th className="px-3 py-2 font-medium max-md:hidden">Environment</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {events.data?.events.map((ev) => (
            <tr key={ev.event_id} className="hover:bg-muted/40">
              <td className="max-w-0 px-3 py-2">
                <Link to={`/issues/${issueId}/events/${ev.event_id}`} className="block truncate font-mono text-xs text-primary hover:underline">
                  {ev.event_id.slice(0, 12)}
                </Link>
                <span className="block truncate text-xs text-muted-foreground">{ev.message}</span>
              </td>
              <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">
                <Time iso={ev.occurred_at}>{ago(ev.occurred_at)}</Time>
              </td>
              <td className="max-w-40 truncate px-3 py-2 text-xs max-md:hidden">{ev.release ? <Release release={ev.release} repo={repo} /> : "—"}</td>
              <td className="px-3 py-2 max-md:hidden">{ev.environment || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const chartConfig = { events: { label: "Events", color: "var(--chart-1)" } } satisfies ChartConfig

function IssueSidebar({ issue, repo }: { issue: IssueDetail; repo?: string }) {
  const [range, setRange] = useState<"24h" | "30d">("24h")
  const series = range === "24h" ? issue.hourly : issue.daily
  // A tag with one value says nothing about this issue that the event itself
  // doesn't already show; only distributions are worth the space here.
  const varying = (issue.tags ?? []).filter((t) => t.values.length > 1 || t.values[0]?.count !== t.total)
  return (
    <aside className="space-y-6 lg:border-l lg:pl-8">
      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Events</h3>
          <ToggleGroup variant="outline" size="sm" spacing={0} value={[range]} onValueChange={(v) => v[0] && setRange(v[0] as "24h" | "30d")}>
            <ToggleGroupItem value="24h" className="h-6 px-2 text-[11px]">
              24h
            </ToggleGroupItem>
            <ToggleGroupItem value="30d" className="h-6 px-2 text-[11px]">
              30d
            </ToggleGroupItem>
          </ToggleGroup>
        </div>
        <div className="flex gap-6">
          <Stat label="Last 24 hours" value={count(issue.events_24h)} />
          <Stat label="Last 30 days" value={count(issue.events_30d)} />
        </div>
        <EventChart series={series} />
      </section>

      <section className="space-y-2.5 text-sm">
        <SeenRow label="Last seen" at={issue.last_seen} release={issue.last_release} repo={repo} />
        <SeenRow label="First seen" at={issue.first_seen} release={issue.first_release} repo={repo} />
        {issue.environments.length > 0 && (
          <div className="flex flex-wrap gap-1 pt-1">
            {issue.environments.map((e) => (
              <span key={e} className="rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                {e}
              </span>
            ))}
          </div>
        )}
      </section>

      {varying.length > 0 && (
        <section className="space-y-4">
          <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Tags</h3>
          {varying.map((t) => (
            <TagBars key={t.key} tag={t} />
          ))}
        </section>
      )}

      <ActivityLog issue={issue} repo={repo} />
    </aside>
  )
}

const ACTIVITY_SHOWN = 8

function ActivityLog({ issue, repo }: { issue: IssueDetail; repo?: string }) {
  const activity = issue.activity ?? []
  const [all, setAll] = useState(false)
  const shown = all ? activity : activity.slice(0, ACTIVITY_SHOWN)
  return (
    <section className="space-y-3">
      <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">Activity</h3>
      <CommentBox issueId={issue.id} />
      <ol className="space-y-3 border-l pl-4">
        {shown.map((a) => (
          <li key={a.id} className="relative min-w-0 text-sm">
            <span
              className={cn(
                "absolute top-1.5 -left-[20.5px] size-2 rounded-full ring-4 ring-background",
                a.kind === "regressed" ? "bg-level-warning" : a.kind === "first_seen" ? "bg-primary" : a.kind === "comment" ? "bg-foreground/60" : "bg-border"
              )}
            />
            {a.kind === "comment" ? <Comment issueId={issue.id} a={a} /> : <p className={cn("break-words", a.kind === "regressed" && "font-medium")}>{activityText(a, repo)}</p>}
            <Time iso={a.at} className="text-xs text-muted-foreground">
              {ago(a.at)}
            </Time>
          </li>
        ))}
      </ol>
      {activity.length > ACTIVITY_SHOWN && (
        <Button variant="link" size="sm" className="h-auto px-0 text-xs" onClick={() => setAll(!all)}>
          {all ? "Show less" : `Show ${activity.length - ACTIVITY_SHOWN} older`}
        </Button>
      )}
    </section>
  )
}

function CommentBox({ issueId }: { issueId: number }) {
  const [body, setBody] = useState("")
  const qc = useQueryClient()
  const add = useMutation({
    mutationFn: () => api.addComment(issueId, body.trim()),
    onSuccess: () => {
      setBody("")
      qc.invalidateQueries({ queryKey: ["issue", issueId] })
    },
    onError: (e) => toast.error(e.message),
  })
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (body.trim()) add.mutate()
      }}
    >
      <Textarea
        value={body}
        maxLength={2000}
        rows={2}
        placeholder="Add a note for the team"
        className="min-h-14 resize-none text-sm"
        onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && body.trim()) add.mutate()
        }}
      />
      {body.trim() && (
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={add.isPending}>
            Comment
          </Button>
        </div>
      )}
    </form>
  )
}

function Comment({ issueId, a }: { issueId: number; a: Activity }) {
  const me = useMe().data
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => api.deleteComment(issueId, a.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["issue", issueId] }),
    onError: (e) => toast.error(e.message),
  })
  const canDelete = me && (me.is_admin || me.id === a.user_id)
  return (
    <div className="group space-y-1">
      <div className="flex items-center gap-1.5">
        <UserAvatar name={a.actor} />
        <span className="font-medium">{a.actor}</span>
        {canDelete && (
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Delete comment"
            className="ml-auto text-muted-foreground opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
            onClick={() => remove.mutate()}
            disabled={remove.isPending}
          >
            <Trash2Icon />
          </Button>
        )}
      </div>
      <p className="rounded-lg bg-muted/60 px-2.5 py-1.5 break-words whitespace-pre-wrap">{a.detail?.body}</p>
    </div>
  )
}

const alertNames: Record<string, string> = { new_issue: "New issue", regression: "Regression", frequency: "Spike" }

function activityText(a: Activity, repo?: string): ReactNode {
  switch (a.kind) {
    case "first_seen":
      return "First seen"
    case "regressed":
      return a.detail?.release ? (
        <>
          Regressed in <Release release={a.detail.release} repo={repo} />
        </>
      ) : (
        "Regressed"
      )
    case "linked": {
      const id = a.detail?.identifier ?? "an issue"
      const link = a.detail?.url ? (
        <a href={a.detail.url} target="_blank" rel="noreferrer" className="font-medium hover:underline">
          {id}
        </a>
      ) : (
        id
      )
      return a.actor.startsWith("Alert: ") ? (
        <>
          Alert “{a.actor.slice(7)}” filed {link} in Linear
        </>
      ) : (
        <>
          {a.actor} filed {link} in Linear
        </>
      )
    }
    case "resolved":
      if (a.actor === "Linear" && a.detail?.identifier) return `Resolved because ${a.detail.identifier} was completed in Linear`
      return `${a.actor} resolved this issue`
    case "unresolved":
      return `${a.actor} reopened this issue`
    case "muted":
      return `${a.actor} muted this issue`
    case "assigned":
      return a.detail?.assignee === a.actor ? `${a.actor} took this issue` : `${a.actor} assigned this to ${a.detail?.assignee ?? "someone"}`
    case "unassigned":
      return `${a.actor} unassigned this issue`
    case "alerted":
      return `${alertNames[a.detail?.alert ?? ""] ?? "Alert"} sent to ${a.detail?.channel ?? "Slack"}`
    default:
      return `${a.actor} resolved this issue`
  }
}

/** On phones the sidebar sits below the stack trace; this keeps the essentials at the top. */
function PhoneSummary({ issue }: { issue: IssueDetail }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground lg:hidden">
      <span>
        <span className="font-medium text-foreground tabular-nums">{count(issue.times_seen)}</span> events
      </span>
      <span>
        Last seen{" "}
        <Time iso={issue.last_seen} className="text-foreground">
          {ago(issue.last_seen)}
        </Time>
      </span>
      <span>
        First seen{" "}
        <Time iso={issue.first_seen} className="text-foreground">
          {ago(issue.first_seen)}
        </Time>
      </span>
    </div>
  )
}

const linearStates: Record<string, string> = {
  triage: "Triage",
  backlog: "Backlog",
  unstarted: "Todo",
  started: "In progress",
  completed: "Done",
  canceled: "Canceled",
  duplicate: "Duplicate",
}

/** Files the issue in Linear, or links to it once filed. */
function LinearAction({ issue }: { issue: IssueDetail }) {
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: (channelId: number) => api.createLinearIssue(issue.id, channelId),
    onSuccess: (l) => {
      toast.success(`Filed ${l.identifier} in Linear`)
      qc.invalidateQueries({ queryKey: ["issue", issue.id] })
    },
    onError: (e) => toast.error(e.message),
  })
  const link = issue.links?.find((l) => l.provider === "linear")
  if (link) {
    return (
      <Button variant="outline" nativeButton={false} render={<a href={link.url} target="_blank" rel="noreferrer" />}>
        <SquareKanbanIcon />
        <span className="font-mono text-xs">{link.identifier}</span>
        {linearStates[link.state] && <span className="text-xs text-muted-foreground">{linearStates[link.state]}</span>}
        <ArrowUpRightIcon className="text-muted-foreground" />
      </Button>
    )
  }
  const trackers = issue.trackers ?? []
  if (trackers.length === 0) return null
  if (trackers.length === 1) {
    return (
      <Button variant="outline" onClick={() => create.mutate(trackers[0].id)} disabled={create.isPending}>
        <SquareKanbanIcon /> {create.isPending ? "Filing…" : "Create Linear issue"}
      </Button>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="outline" disabled={create.isPending} />}>
        <SquareKanbanIcon /> {create.isPending ? "Filing…" : "Create Linear issue"}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuGroup>
          <DropdownMenuLabel>File in</DropdownMenuLabel>
          {trackers.map((t) => (
            <DropdownMenuItem key={t.id} onClick={() => create.mutate(t.id)}>
              {t.team}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function AssigneePicker({ issue }: { issue: IssueDetail }) {
  const me = useMe().data
  const members = useQuery({ queryKey: ["members"], queryFn: api.members, staleTime: 60_000 })
  const qc = useQueryClient()
  const assign = useMutation({
    mutationFn: (userId: number | null) => api.setAssignee(issue.id, userId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["issue", issue.id] })
      qc.invalidateQueries({ queryKey: ["issues"] })
    },
    onError: (e) => toast.error(e.message),
  })
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="outline" disabled={assign.isPending} />}>
        {issue.assignee ? <UserAvatar name={issue.assignee} /> : <UserRoundIcon />}
        <span className="max-w-32 truncate">{issue.assignee || "Assign"}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        {me && issue.assignee_id !== me.id && (
          <>
            <DropdownMenuItem onClick={() => assign.mutate(me.id)}>
              <UserAvatar name={me.name || me.email} />
              Assign to me
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuGroup>
          <DropdownMenuLabel>Members</DropdownMenuLabel>
          {members.data?.members.map((m) => (
            <DropdownMenuItem key={m.id} onClick={() => assign.mutate(m.id)}>
              <UserAvatar name={m.name || m.email} />
              <span className="truncate">{m.name || m.email}</span>
              {issue.assignee_id === m.id && <CheckIcon className="ml-auto" />}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
        {issue.assignee_id && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => assign.mutate(null)}>Unassign</DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  )
}

function SeenRow({ label, at, release, repo }: { label: string; at: string; release: string; repo?: string }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="flex flex-wrap items-baseline gap-x-2">
        <Time iso={at} className="font-medium">
          {ago(at)}
        </Time>
        {release && (
          <span className="text-xs text-muted-foreground">
            in <Release release={release} repo={repo} />
          </span>
        )}
      </div>
    </div>
  )
}

function EventChart({ series }: { series: Series }) {
  const start = new Date(series.start)
  const data = series.counts.map((n, i) => {
    const t = series.bucket === "hour" ? addHours(start, i) : addDays(start, i)
    return { t: format(t, series.bucket === "hour" ? "HH:mm" : "MMM d"), full: dateTime(t.toISOString()), events: n }
  })
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-28 w-full">
      <BarChart data={data} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
        <XAxis dataKey="t" tickLine={false} axisLine={false} interval="preserveStartEnd" minTickGap={40} fontSize={10} />
        <ChartTooltip cursor={false} content={<ChartTooltipContent labelKey="full" />} />
        <Bar dataKey="events" fill="var(--color-events)" radius={2} />
      </BarChart>
    </ChartContainer>
  )
}

function TagBars({ tag }: { tag: TagSummary }) {
  const shown = tag.values.reduce((a, v) => a + v.count, 0)
  const other = tag.total - shown
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium">{tag.key}</div>
      <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
        {tag.values.map((v, i) => (
          <div key={v.value} style={{ width: `${(v.count / tag.total) * 100}%`, opacity: 1 - i * 0.17 }} className="bg-chart-1" />
        ))}
      </div>
      <ul className="space-y-0.5">
        {tag.values.map((v) => (
          <li key={v.value} className="flex items-center justify-between gap-2 text-xs">
            <span className="truncate font-mono text-muted-foreground" title={v.value}>
              {tag.key === "release" ? shortRelease(v.value) : v.value}
            </span>
            <span className="shrink-0 text-muted-foreground tabular-nums">{percent(v.count, tag.total)}</span>
          </li>
        ))}
        {other > 0 && (
          <li className="flex justify-between text-xs text-muted-foreground/70">
            <span>Other</span>
            <span className="tabular-nums">{percent(other, tag.total)}</span>
          </li>
        )}
      </ul>
    </div>
  )
}

function IssueSkeleton() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-4 w-48" />
      <Skeleton className="h-7 w-2/3" />
      <Skeleton className="h-4 w-1/3" />
      <Skeleton className="mt-8 h-96 w-full" />
    </div>
  )
}
