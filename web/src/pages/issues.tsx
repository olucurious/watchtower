import { useEffect, useMemo, useRef, useState } from "react"
import { Link, useSearchParams } from "react-router"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { BellOffIcon, CheckIcon, ChevronLeftIcon, ChevronRightIcon, CircleCheckBigIcon, RotateCcwIcon, SearchIcon } from "lucide-react"
import { cn } from "cn"

import { api, type IssuePage, type IssueRow, type IssueStatus } from "@/lib/api"
import { count, shortAgo, splitTitle } from "@/lib/format"
import { EmptyState, LevelStripe, PageHeader, Release, Sparkline, Time, UserAvatar, useProjectRepo } from "@/components/bits"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

const PAGE = 25
const periods = ["24h", "7d", "14d", "30d"] as const
const assignees = { __any: "Anyone", me: "Assigned to me", none: "Unassigned" }
const sorts = { last_seen: "Last seen", first_seen: "First seen", times_seen: "Events" }
const tabs = [
  ["unresolved", "Unresolved"],
  ["resolved", "Resolved"],
  ["muted", "Muted"],
  ["all", "All"],
] as const

export function IssuesPage() {
  const [params, setParams] = useSearchParams()
  const filters = {
    project: params.get("project") ?? "",
    environment: params.get("environment") ?? "",
    period: params.get("period") ?? "14d",
    status: params.get("status") ?? "unresolved",
    sort: params.get("sort") ?? "last_seen",
    query: params.get("query") ?? "",
    assignee: params.get("assignee") ?? "",
    offset: Number(params.get("offset") ?? 0),
  }
  const update = (patch: Partial<Record<keyof typeof filters, string | number>>) => {
    const next = new URLSearchParams(params)
    for (const [k, v] of Object.entries(patch)) {
      if (v === "" || v === undefined) next.delete(k)
      else next.set(k, String(v))
    }
    if (!("offset" in patch)) next.delete("offset")
    setParams(next, { replace: true })
  }

  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects })
  const envs = useQuery({ queryKey: ["environments", filters.project], queryFn: () => api.environments(filters.project) })
  const issues = useQuery({
    queryKey: ["issues", filters],
    queryFn: () => api.issues({ ...filters, limit: PAGE }),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
  // Changing the filters clears the selection (adjusted during render,
  // so there is no extra render with a stale selection).
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [selectionParams, setSelectionParams] = useState(params)
  if (params !== selectionParams) {
    setSelectionParams(params)
    setSelected(new Set())
  }

  return (
    <div className="space-y-5">
      <PageHeader title="Issues" description="Errors grouped by cause, across every connected SDK." />

      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={filters.project || "__all"}
          onValueChange={(v) => update({ project: v === "__all" ? "" : String(v), environment: "" })}
          items={[{ value: "__all", label: "All projects" }, ...(projects.data?.projects ?? []).map((p) => ({ value: p.slug, label: p.name }))]}
        >
          <SelectTrigger className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All projects</SelectItem>
            {projects.data?.projects.map((p) => (
              <SelectItem key={p.slug} value={p.slug}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={filters.environment || "__all"}
          onValueChange={(v) => update({ environment: v === "__all" ? "" : String(v) })}
          items={[{ value: "__all", label: "All environments" }, ...(envs.data?.environments ?? []).map((e) => ({ value: e, label: e }))]}
        >
          <SelectTrigger className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all">All environments</SelectItem>
            {envs.data?.environments.map((e) => (
              <SelectItem key={e} value={e}>
                {e}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={filters.assignee || "__any"}
          onValueChange={(v) => update({ assignee: v === "__any" ? "" : String(v) })}
          items={assignees}
        >
          <SelectTrigger className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {Object.entries(assignees).map(([v, label]) => (
              <SelectItem key={v} value={v}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <ToggleGroup
          variant="outline"
          spacing={0}
          value={[filters.period]}
          onValueChange={(v) => v[0] && update({ period: v[0] as string })}
        >
          {periods.map((p) => (
            <ToggleGroupItem key={p} value={p} className="px-3 text-xs">
              {p}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <SearchBox value={filters.query} onChange={(query) => update({ query })} />
      </div>

      <div className="overflow-hidden rounded-xl border bg-card">
        <div className="flex flex-wrap items-center justify-between gap-x-3 border-b px-3">
          <Tabs value={filters.status} onValueChange={(v) => update({ status: String(v) })} className="max-w-full min-w-0 overflow-x-auto [scrollbar-width:none]">
            <TabsList variant="line" className="h-11 w-max">
              {tabs.map(([value, label]) => (
                <TabsTrigger key={value} value={value} className="px-2.5 max-sm:px-1.5">
                  {label}
                  <StatusCount page={issues.data} status={value} />
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <Select value={filters.sort} onValueChange={(v) => update({ sort: String(v) })} items={sorts}>
            <SelectTrigger size="sm" className="w-auto border-none bg-transparent text-xs text-muted-foreground shadow-none dark:bg-transparent">
              <span className="text-muted-foreground/70">Sort:</span>
              <SelectValue />
            </SelectTrigger>
            <SelectContent align="end">
              {Object.entries(sorts).map(([v, label]) => (
                <SelectItem key={v} value={v}>
                  {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {issues.error ? (
          <p role="alert" className="p-4 text-sm text-destructive">{issues.error.message}</p>
        ) : (
          <IssueTable page={issues.data} loading={issues.isPending} selected={selected} setSelected={setSelected} filters={filters} />
        )}

        {issues.data && issues.data.total > PAGE && (
          <div className="flex items-center justify-between border-t px-4 py-2.5 text-xs text-muted-foreground">
            <span>
              {filters.offset + 1}–{Math.min(filters.offset + PAGE, issues.data.total)} of {count(issues.data.total)}
            </span>
            <div className="flex gap-1">
              <Button variant="outline" size="icon-sm" aria-label="Previous page" disabled={filters.offset === 0} onClick={() => update({ offset: Math.max(0, filters.offset - PAGE) })}>
                <ChevronLeftIcon />
              </Button>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="Next page"
                disabled={filters.offset + PAGE >= issues.data.total}
                onClick={() => update({ offset: filters.offset + PAGE })}
              >
                <ChevronRightIcon />
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function StatusCount({ page, status }: { page?: IssuePage; status: string }) {
  if (!page) return null
  const n = status === "all" ? Object.values(page.counts).reduce((a, b) => a + (b ?? 0), 0) : (page.counts[status as IssueStatus] ?? 0)
  // Phones have room for every tab only if just the actionable count is shown.
  return (
    <span className={cn("ml-1.5 rounded-full bg-muted px-1.5 py-px text-[11px] font-medium text-muted-foreground tabular-nums", status !== "unresolved" && "max-sm:hidden")}>
      {count(n)}
    </span>
  )
}

/** Debounced search; "/" focuses it from anywhere on the page. */
function SearchBox({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [text, setText] = useState(value)
  const ref = useRef<HTMLInputElement>(null)
  // A new value from outside (e.g. back navigation) replaces the text.
  const [outside, setOutside] = useState(value)
  if (value !== outside) {
    setOutside(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const t = setTimeout(() => onChange(text), 250)
    return () => clearTimeout(t)
  }, [text]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "/" && !(e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement)) {
        e.preventDefault()
        ref.current?.focus()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  return (
    <div className="relative min-w-56 flex-1">
      <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
      <Input ref={ref} value={text} onChange={(e) => setText(e.target.value)} placeholder="Search titles and code locations" className="pl-8" />
      <kbd className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 rounded border bg-muted px-1.5 text-[10px] text-muted-foreground">/</kbd>
    </div>
  )
}

function IssueTable({
  page,
  loading,
  selected,
  setSelected,
  filters,
}: {
  page?: IssuePage
  loading: boolean
  selected: Set<number>
  setSelected: (s: Set<number>) => void
  filters: { status: string; query: string; period: string }
}) {
  const qc = useQueryClient()
  const setStatus = useMutation({
    mutationFn: (status: IssueStatus) => api.setStatus([...selected], status),
    onSuccess: (res, status) => {
      toast.success(`${res.updated} issue${res.updated === 1 ? "" : "s"} marked ${status}`)
      setSelected(new Set())
      qc.invalidateQueries({ queryKey: ["issues"] })
    },
    onError: (e) => toast.error(e.message),
  })
  const ids = useMemo(() => page?.issues.map((i) => i.id) ?? [], [page])
  const all = ids.length > 0 && ids.every((id) => selected.has(id))

  return (
    <div>
      <div className="grid grid-cols-[2rem_minmax(0,1fr)_8rem_4.5rem_4.5rem] items-center gap-4 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase max-md:grid-cols-[2rem_minmax(0,1fr)_4.5rem]">
        <Checkbox
          aria-label="Select all"
          checked={all}
          indeterminate={!all && selected.size > 0}
          onCheckedChange={(c) => setSelected(c ? new Set(ids) : new Set())}
        />
        {selected.size > 0 ? (
          <div className="flex items-center gap-1 normal-case">
            <span className="mr-2 tracking-normal">{selected.size} selected</span>
            {filters.status !== "resolved" && (
              <Button size="xs" variant="outline" onClick={() => setStatus.mutate("resolved")}>
                <CheckIcon /> Resolve
              </Button>
            )}
            {filters.status !== "muted" && (
              <Button size="xs" variant="outline" onClick={() => setStatus.mutate("muted")}>
                <BellOffIcon /> Mute
              </Button>
            )}
            {filters.status !== "unresolved" && (
              <Button size="xs" variant="outline" onClick={() => setStatus.mutate("unresolved")}>
                <RotateCcwIcon /> Unresolve
              </Button>
            )}
          </div>
        ) : (
          <span>Issue</span>
        )}
        <span className="max-md:hidden">Graph · {page ? `${page.trend.size}${page.trend.bucket === "hour" ? "h" : "d"}` : ""}</span>
        <span className="text-right">Events</span>
        <span className="text-right max-md:hidden">Seen</span>
      </div>

      {loading && <IssueSkeleton />}
      {page && page.issues.length === 0 && (
        <EmptyState icon={<CircleCheckBigIcon />} title={filters.query ? "No issues match your search" : "No issues here"}>
          {filters.status === "unresolved" && !filters.query
            ? "Nothing unresolved in this period. New errors appear here as soon as an SDK reports them."
            : "Try a different status, period or search."}
        </EmptyState>
      )}
      <ul className="divide-y">
        {page?.issues.map((issue) => (
          <IssueItem
            key={issue.id}
            issue={issue}
            checked={selected.has(issue.id)}
            onCheck={(c) => {
              const next = new Set(selected)
              if (c) next.add(issue.id)
              else next.delete(issue.id)
              setSelected(next)
            }}
          />
        ))}
      </ul>
    </div>
  )
}

function IssueItem({ issue, checked, onCheck }: { issue: IssueRow; checked: boolean; onCheck: (c: boolean) => void }) {
  const { type, value } = splitTitle(issue.title)
  const repo = useProjectRepo(issue.project)
  return (
    <li className="group relative grid grid-cols-[2rem_minmax(0,1fr)_8rem_4.5rem_4.5rem] items-center gap-4 px-3 py-3 transition-colors hover:bg-muted/40 has-[[data-checked]]:bg-accent/50 max-md:grid-cols-[2rem_minmax(0,1fr)_4.5rem]">
      <LevelStripe level={issue.level} className="absolute top-2.5 bottom-2.5 left-0" />
      <Checkbox aria-label="Select issue" checked={checked} onCheckedChange={(c) => onCheck(!!c)} className="relative z-10" />
      <div className="min-w-0">
        <Link to={`/issues/${issue.id}`} className="flex min-w-0 flex-wrap items-baseline gap-x-2 after:absolute after:inset-0 md:flex-nowrap">
          <span className="max-w-full shrink-0 truncate font-semibold text-foreground group-hover:text-primary">{type}</span>
          {value && <span className="max-w-full min-w-0 truncate text-sm text-muted-foreground">{value}</span>}
        </Link>
        {issue.culprit && <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground/90">{issue.culprit}</p>}
        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span className="font-medium text-foreground/80">{issue.project}</span>
          {issue.regressed_at && issue.status === "unresolved" && (
            <Badge className="h-4.5 bg-level-warning/15 px-1.5 text-[10px] text-[oklch(0.5_0.13_60)] dark:text-level-warning">Regressed</Badge>
          )}
          {issue.status === "muted" && <Badge variant="secondary" className="h-4.5 px-1.5 text-[10px]">Muted</Badge>}
          {issue.status === "resolved" && (
            <Badge className="h-4.5 bg-emerald-500/12 px-1.5 text-[10px] text-emerald-700 dark:text-emerald-400">Resolved</Badge>
          )}
          <Time iso={issue.first_seen}>First seen {shortAgo(issue.first_seen)} ago</Time>
          {issue.last_release && <Release release={issue.last_release} repo={repo} className="truncate" />}
          {issue.assignee && (
            <span className="inline-flex min-w-0 items-center gap-1">
              <UserAvatar name={issue.assignee} className="size-4" />
              <span className="truncate">{issue.assignee}</span>
            </span>
          )}
        </div>
      </div>
      <div className="max-md:hidden">{issue.trend && <Sparkline values={issue.trend} />}</div>
      <div className="text-right text-sm font-medium tabular-nums">{count(issue.times_seen)}</div>
      <div className="text-right text-xs text-muted-foreground tabular-nums max-md:hidden">
        <Time iso={issue.last_seen}>{shortAgo(issue.last_seen)}</Time>
      </div>
    </li>
  )
}

function IssueSkeleton() {
  return (
    <div className="divide-y">
      {Array.from({ length: 6 }, (_, i) => (
        <div key={i} className="flex items-center gap-4 px-3 py-4">
          <Skeleton className="size-4" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-2/5" />
            <Skeleton className="h-3 w-1/4" />
          </div>
          <Skeleton className="h-6 w-28" />
        </div>
      ))}
    </div>
  )
}
