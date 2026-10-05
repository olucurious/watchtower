import { useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { CheckIcon, CopyIcon } from "lucide-react"
import { cn } from "cn"

import { api, type IssueStatus, type Level } from "@/lib/api"
import { commitURL, dateTime, initials, shortRelease } from "@/lib/format"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

const levelColor: Record<Level, string> = {
  fatal: "bg-level-fatal",
  error: "bg-level-error",
  warning: "bg-level-warning",
  info: "bg-level-info",
  debug: "bg-level-debug",
}

/** The coloured bar at the left edge of an issue row. */
export function LevelStripe({ level, className }: { level: Level; className?: string }) {
  return <span aria-hidden className={cn("w-[3px] shrink-0 rounded-full", levelColor[level] ?? levelColor.error, className)} />
}

export function LevelBadge({ level }: { level: Level }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground capitalize">
      <span className={cn("size-2 rounded-full", levelColor[level] ?? levelColor.error)} />
      {level}
    </span>
  )
}

export function StatusBadge({ status, regressed }: { status: IssueStatus; regressed?: boolean }) {
  if (status === "unresolved" && regressed)
    return <Badge className="bg-level-warning/15 text-[oklch(0.5_0.13_60)] dark:text-level-warning">Regressed</Badge>
  if (status === "resolved") return <Badge className="bg-emerald-500/12 text-emerald-700 dark:text-emerald-400">Resolved</Badge>
  if (status === "muted") return <Badge variant="secondary">Muted</Badge>
  return <Badge variant="outline">Unresolved</Badge>
}

/** Event-count bars for an issue row; empty buckets render as a baseline. */
export function Sparkline({ values, className }: { values: number[]; className?: string }) {
  const max = Math.max(1, ...values)
  const w = 100 / values.length
  return (
    <svg viewBox="0 0 100 24" preserveAspectRatio="none" className={cn("h-6 w-full", className)} aria-hidden>
      {values.map((v, i) => {
        const h = v === 0 ? 1 : Math.max(2.5, (v / max) * 24)
        return (
          <rect
            key={i}
            x={i * w + w * 0.12}
            y={24 - h}
            width={w * 0.76}
            height={h}
            rx={0.6}
            className={v === 0 ? "fill-border" : "fill-chart-1/80"}
          />
        )
      })}
    </svg>
  )
}

export function Time({ iso, children, className }: { iso: string; children: ReactNode; className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger render={<time dateTime={iso} className={cn("cursor-default", className)} />}>{children}</TooltipTrigger>
      <TooltipContent>{dateTime(iso)}</TooltipContent>
    </Tooltip>
  )
}

export function CopyButton({ value, label = "Copy", className }: { value: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      variant="ghost"
      size="icon-xs"
      aria-label={label}
      className={className}
      onClick={async () => {
        await navigator.clipboard.writeText(value)
        setDone(true)
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? <CheckIcon className="text-emerald-600" /> : <CopyIcon />}
    </Button>
  )
}

export function EmptyState({ icon, title, children }: { icon: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-16 text-center">
      <div className="mb-1 flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground [&_svg]:size-5">{icon}</div>
      <p className="text-sm font-medium">{title}</p>
      {children && <div className="max-w-sm text-sm text-muted-foreground">{children}</div>}
    </div>
  )
}

export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" className={cn("size-5", className)} aria-hidden>
      <path d="M12 2 4 6v3h16V6l-8-4Z" fill="currentColor" opacity=".9" />
      <path d="M6 10h12l-1.2 12H7.2L6 10Z" fill="currentColor" opacity=".55" />
      <path d="M10 14h4v8h-4z" fill="currentColor" />
    </svg>
  )
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

/** The repository URL configured for a project, used for commit links. */
export function useProjectRepo(slug: string | undefined): string | undefined {
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects, staleTime: 60_000 })
  return projects.data?.projects.find((p) => p.slug === slug)?.repo_url || undefined
}

/**
 * A release name, abbreviated when it is a commit SHA. The full value is in
 * the tooltip, and a SHA links to its commit when the project names a repository.
 */
export function Release({ release, repo, className }: { release: string; repo?: string; className?: string }) {
  const short = shortRelease(release)
  const href = commitURL(repo, release)
  const text = <span className={cn("font-mono", className)}>{short}</span>
  if (short === release && !href) return text
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          href ? (
            <a href={href} target="_blank" rel="noreferrer" className="underline decoration-muted-foreground/50 decoration-dotted underline-offset-2 hover:text-primary hover:decoration-solid" />
          ) : (
            <span className="cursor-default" />
          )
        }
      >
        {text}
      </TooltipTrigger>
      <TooltipContent className="font-mono">{release}</TooltipContent>
    </Tooltip>
  )
}

export function UserAvatar({ name, className }: { name: string; className?: string }) {
  return (
    <Avatar className={cn("size-5 rounded-full", className)}>
      <AvatarFallback className="rounded-full bg-primary/15 text-[9px] font-semibold text-primary">{initials(name)}</AvatarFallback>
    </Avatar>
  )
}
