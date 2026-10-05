import { useState, type ReactNode } from "react"
import { Link } from "react-router"
import { useQuery } from "@tanstack/react-query"
import { ArrowRightIcon, CheckIcon } from "lucide-react"
import { cn } from "cn"

import { api } from "@/lib/api"
import { CopyButton } from "@/components/bits"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"

type Platform = "elixir" | "node" | "python" | "browser" | "appsignal"

const platforms: [Platform, string][] = [
  ["elixir", "Elixir"],
  ["node", "Node.js"],
  ["python", "Python"],
  ["browser", "Browser"],
  ["appsignal", "Already on AppSignal"],
]

const install: Record<Exclude<Platform, "appsignal">, { file: string; code: string }[]> = {
  elixir: [
    { file: "mix.exs", code: `{:sentry, "~> 13.4"}` },
    {
      file: "config/runtime.exs",
      code: `config :sentry,
  dsn: System.get_env("SENTRY_DSN"),
  environment_name: config_env()`,
    },
    {
      file: "lib/my_app/application.ex, in start/2",
      code: `:logger.add_handler(:sentry_handler, Sentry.LoggerHandler, %{
  config: %{metadata: [:mfa, :application]}
})`,
    },
  ],
  node: [
    { file: "Terminal", code: "npm install @sentry/node" },
    {
      file: "instrument.js, loaded before the rest of the app",
      code: `const Sentry = require("@sentry/node")
Sentry.init({ dsn: process.env.SENTRY_DSN, environment: process.env.NODE_ENV })`,
    },
  ],
  python: [
    { file: "Terminal", code: "pip install sentry-sdk" },
    {
      file: "At startup",
      code: `import os, sentry_sdk
sentry_sdk.init(dsn=os.environ["SENTRY_DSN"], environment="production")`,
    },
  ],
  browser: [
    { file: "Terminal", code: "npm install @sentry/browser" },
    {
      file: "Your entry point",
      code: `import * as Sentry from "@sentry/browser"
Sentry.init({ dsn: "<the DSN from your key>" })`,
    },
  ],
}

const testCall: Record<Exclude<Platform, "appsignal">, string> = {
  elixir: `Sentry.capture_message("Hello from Watchtower")`,
  node: `Sentry.captureMessage("Hello from Watchtower")`,
  python: `sentry_sdk.capture_message("Hello from Watchtower")`,
  browser: `Sentry.captureMessage("Hello from Watchtower")`,
}

/**
 * First-run guide for a project with no events: create a key, add the SDK,
 * send a test error. It watches for the first event and links to it.
 */
export function GetStarted({ slug, hasKey, keyAction }: { slug: string; hasKey: boolean; keyAction: ReactNode }) {
  const [platform, setPlatform] = useState<Platform>("elixir")
  const first = useQuery({
    queryKey: ["first-issue", slug],
    queryFn: () => api.issues({ project: slug, status: "all", limit: 1 }),
    refetchInterval: (q) => (q.state.data?.total ? false : 5_000),
  })
  const received = first.data?.issues[0]

  return (
    <section className="space-y-5 rounded-xl border bg-card p-5">
      <div>
        <h2 className="font-semibold">Get started</h2>
        <p className="text-sm text-muted-foreground">Point an SDK at this project and send a first error. Any Sentry SDK works without code changes beyond the DSN.</p>
      </div>
      <ol className="space-y-6">
        <Step n={1} done={hasKey} title="Create a key">
          {/* keyAction stays mounted once a key exists: it holds the dialog
              that shows the new, one-time DSN. */}
          <div className="flex flex-wrap items-center gap-3">
            {keyAction}
            <span className="text-sm text-muted-foreground">
              {hasKey
                ? "This project has a key. Its DSN goes in the service's environment as SENTRY_DSN."
                : "The DSN is shown once; put it in the service's environment as SENTRY_DSN."}
            </span>
          </div>
        </Step>
        <Step n={2} title="Add the SDK">
          <Tabs value={platform} onValueChange={(v) => setPlatform(v as Platform)} className="max-w-full overflow-x-auto [scrollbar-width:none]">
            <TabsList className="w-max">
              {platforms.map(([value, label]) => (
                <TabsTrigger key={value} value={value} className="px-2.5 text-xs">
                  {label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          {platform === "appsignal" ? (
            <p className="text-sm text-muted-foreground">
              Keep your AppSignal integration as it is. Create a key for AppSignal (or the AppSignal browser SDK) and change the two settings it shows: the push API key
              and the endpoint. Errors arrive here instead of AppSignal.
            </p>
          ) : (
            <div className="space-y-2">
              {install[platform].map((s) => (
                <Code key={s.file} label={s.file} code={s.code} />
              ))}
            </div>
          )}
        </Step>
        <Step n={3} done={!!received} title="Send a test error">
          {platform !== "appsignal" && <Code label="From a console or anywhere in the app" code={testCall[platform]} />}
          {received ? (
            <Button nativeButton={false} render={<Link to={`/issues/${received.id}`} />} size="sm">
              First event received. View it <ArrowRightIcon />
            </Button>
          ) : (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <span className="relative flex size-2">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary/60" />
                <span className="relative inline-flex size-2 rounded-full bg-primary" />
              </span>
              Waiting for the first event…
            </p>
          )}
        </Step>
      </ol>
    </section>
  )
}

function Step({ n, title, done, children }: { n: number; title: string; done?: boolean; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-3">
      <span
        className={cn(
          "flex size-7 items-center justify-center rounded-full border text-xs font-semibold",
          done ? "border-transparent bg-primary text-primary-foreground" : "text-muted-foreground"
        )}
      >
        {done ? <CheckIcon className="size-3.5" /> : n}
      </span>
      <div className="min-w-0 space-y-2.5 pt-0.5">
        <h3 className="text-sm font-medium">{title}</h3>
        {children}
      </div>
    </li>
  )
}

function Code({ label, code }: { label: string; code: string }) {
  return (
    <div className="rounded-lg border bg-muted/40">
      <div className="flex items-center justify-between px-3 pt-2 text-xs text-muted-foreground">
        <span className="font-mono">{label}</span>
        <CopyButton value={code} label={`Copy ${label}`} />
      </div>
      <pre className="overflow-x-auto px-3 pb-2.5 text-xs">
        <code>{code}</code>
      </pre>
    </div>
  )
}
