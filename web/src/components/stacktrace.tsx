import { useState } from "react"
import { ChevronDownIcon, ChevronRightIcon } from "lucide-react"
import { cn } from "cn"

import type { ExceptionValue, Frame } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

type Order = "newest" | "oldest"

/**
 * The exception chain, raised exception first, like Sentry. The raised
 * exception's message is omitted when it is exactly the issue title shown above.
 */
export function Exceptions({ values, title }: { values: ExceptionValue[]; title?: string }) {
  const [order, setOrder] = useState<Order>("newest")
  const [appOnly, setAppOnly] = useState(true)
  const chain = [...values].reverse()
  const anyInApp = values.some((v) => v.frames?.some((f) => f.in_app))
  const anyFrames = values.some((v) => v.frames?.length)
  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">Exception</h2>
        <div className="flex items-center gap-2">
          {anyInApp && (
            <ToggleGroup variant="outline" size="sm" spacing={0} value={[appOnly ? "app" : "full"]} onValueChange={(v) => v[0] && setAppOnly(v[0] === "app")}>
              <ToggleGroupItem value="app" className="px-2.5 text-xs">
                App frames
              </ToggleGroupItem>
              <ToggleGroupItem value="full" className="px-2.5 text-xs">
                Full stack
              </ToggleGroupItem>
            </ToggleGroup>
          )}
          {anyFrames && (
            <ToggleGroup variant="outline" size="sm" spacing={0} value={[order]} onValueChange={(v) => v[0] && setOrder(v[0] as Order)}>
              <ToggleGroupItem value="newest" className="px-2.5 text-xs">
                Newest first
              </ToggleGroupItem>
              <ToggleGroupItem value="oldest" className="px-2.5 text-xs">
                Oldest first
              </ToggleGroupItem>
            </ToggleGroup>
          )}
        </div>
      </div>
      {chain.map((ex, i) => (
        <div key={i} className="space-y-2.5">
          {i > 0 && <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Caused by</p>}
          <div>
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="font-mono text-[15px] font-semibold">{ex.type || "Error"}</span>
              {ex.mechanism?.handled === false && <Badge variant="destructive">Unhandled</Badge>}
              {ex.mechanism?.type && ex.mechanism.type !== "generic" && <Badge variant="outline" className="font-mono text-[10px]">{ex.mechanism.type}</Badge>}
            </div>
            {ex.value && !(i === 0 && title === `${ex.type || "Error"}: ${ex.value}`) && (
              <p className="mt-1 font-mono text-sm break-words whitespace-pre-wrap text-muted-foreground">{ex.value}</p>
            )}
          </div>
          {ex.frames && ex.frames.length > 0 ? (
            <Frames frames={ex.frames} order={order} appOnly={appOnly && anyInApp} />
          ) : (
            <p className="rounded-lg border border-dashed px-3 py-2.5 text-sm text-muted-foreground">
              No stack trace was sent with this exception. That is usual when an error is reported from a log line rather than raised in code.
            </p>
          )}
        </div>
      ))}
    </section>
  )
}

/** Frames with runs of library frames collapsed behind a toggle. */
function Frames({ frames, order, appOnly }: { frames: Frame[]; order: Order; appOnly: boolean }) {
  const list = order === "newest" ? [...frames].reverse() : frames
  const groups: { inApp: boolean; frames: Frame[] }[] = []
  for (const f of list) {
    const last = groups[groups.length - 1]
    if (last && last.inApp === f.in_app) last.frames.push(f)
    else groups.push({ inApp: f.in_app, frames: [f] })
  }
  const crashing = order === "newest" ? 0 : list.length - 1
  let index = 0
  return (
    <div className="overflow-hidden rounded-lg border">
      {groups.map((g, gi) => {
        const start = index
        index += g.frames.length
        if (!g.inApp && appOnly)
          return <CollapsedFrames key={gi} frames={g.frames} startIndex={start} crashing={crashing} />
        return g.frames.map((f, i) => <FrameRow key={`${gi}-${i}`} frame={f} crashing={start + i === crashing} />)
      })}
    </div>
  )
}

function CollapsedFrames({ frames, startIndex, crashing }: { frames: Frame[]; startIndex: number; crashing: number }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="border-b last:border-b-0">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-1.5 bg-muted/40 px-3 py-1.5 text-left text-xs text-muted-foreground hover:bg-muted"
      >
        {open ? <ChevronDownIcon className="size-3.5" /> : <ChevronRightIcon className="size-3.5" />}
        {open ? "Hide" : "Show"} {frames.length} library frame{frames.length === 1 ? "" : "s"}
      </button>
      {open && frames.map((f, i) => <FrameRow key={i} frame={f} crashing={startIndex + i === crashing} />)}
    </div>
  )
}

function FrameRow({ frame, crashing }: { frame: Frame; crashing: boolean }) {
  const where = frame.filename || frame.abs_path || frame.module || "<unknown>"
  const hasSource = !!frame.context_line && !!frame.lineno
  return (
    <div
      className={cn(
        "border-b font-mono text-[12.5px] leading-relaxed last:border-b-0",
        frame.in_app ? "bg-card" : "bg-muted/30 text-muted-foreground",
        crashing && "bg-accent/60 dark:bg-accent/40"
      )}
    >
      <div className="flex flex-wrap items-baseline gap-x-1.5 px-3 py-2">
        <span className={cn("break-all", frame.in_app && "font-medium text-foreground")}>{where}</span>
        {frame.function && (
          <>
            <span className="text-muted-foreground">in</span>
            <span className={cn("break-all", frame.in_app && "text-primary")}>{frame.module && frame.filename ? `${frame.module}.${frame.function}` : frame.function}</span>
          </>
        )}
        {frame.lineno ? (
          <span className="text-muted-foreground">
            at line {frame.lineno}
            {frame.colno ? `:${frame.colno}` : ""}
          </span>
        ) : null}
        <span className="ml-auto flex gap-1 font-sans">
          {frame.minified && (
            <span
              className="rounded bg-muted px-1.5 text-[10px] font-medium text-muted-foreground"
              title={`Minified: ${frame.minified.filename}:${frame.minified.lineno}:${frame.minified.colno}${frame.minified.function ? ` (${frame.minified.function})` : ""}`}
            >
              Source mapped
            </span>
          )}
          {frame.in_app && <span className="rounded bg-primary/10 px-1.5 text-[10px] font-medium text-primary">In app</span>}
        </span>
      </div>
      {hasSource && (crashing || frame.in_app) ? (
        <SourceContext frame={frame} />
      ) : (
        frame.context_line && <pre className="mx-3 mb-2 overflow-x-auto rounded bg-muted/60 px-2 py-1 text-[12px]">{frame.context_line.trim()}</pre>
      )}
    </div>
  )
}

/** The lines around the frame, numbered, with the frame's line highlighted. */
function SourceContext({ frame }: { frame: Frame }) {
  const pre = frame.pre_context ?? []
  const post = frame.post_context ?? []
  const first = frame.lineno! - pre.length
  const lines = [...pre, frame.context_line!, ...post]
  return (
    <div className="mx-3 mb-2.5 overflow-x-auto rounded-md border bg-muted/40 py-1 text-[12px]">
      {lines.map((text, i) => {
        const n = first + i
        const current = n === frame.lineno
        return (
          <div key={n} className={cn("flex min-w-max", current && "bg-primary/10 text-foreground")}>
            <span className="w-12 shrink-0 pr-3 text-right text-muted-foreground/70 select-none">{n}</span>
            <span className={cn("pr-4 whitespace-pre", current && "font-medium")}>{text || " "}</span>
          </div>
        )
      })}
    </div>
  )
}
