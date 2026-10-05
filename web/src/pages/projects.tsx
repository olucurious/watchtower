import { useState } from "react"
import { Link, useParams } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { ArrowRightIcon, FolderPlusIcon, GitBranchIcon, KeyRoundIcon, PlusIcon, TriangleAlertIcon } from "lucide-react"

import { api, type AdapterInfo, type Key } from "@/lib/api"
import { adapterLabel, ago, count } from "@/lib/format"
import { CopyButton, EmptyState, PageHeader, Time } from "@/components/bits"
import { useMe } from "@/components/app-shell"
import { AlertsSection } from "@/components/alerts"
import { SourceMapsSection } from "@/components/source-maps"
import { GetStarted } from "@/components/onboarding"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"

export function ProjectsPage() {
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects })
  const isAdmin = useMe().data?.is_admin
  return (
    <div className="space-y-6">
      <PageHeader title="Projects" description="Each project has its own keys and issues." actions={isAdmin && <NewProjectDialog />} />
      {projects.isPending && <Skeleton className="h-32 w-full" />}
      {projects.data?.projects.length === 0 && (
        <div className="rounded-xl border bg-card">
          <EmptyState icon={<FolderPlusIcon />} title="No projects yet">
            Create a project, then issue a key for the SDK your service already uses.
          </EmptyState>
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {projects.data?.projects.map((p) => (
          <Link key={p.slug} to={`/projects/${p.slug}`} className="group rounded-xl border bg-card p-5 transition-colors hover:border-primary/40 hover:bg-accent/30">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <h2 className="truncate font-semibold">{p.name}</h2>
                <p className="font-mono text-xs text-muted-foreground">{p.slug}</p>
              </div>
              <ArrowRightIcon className="size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="mt-5 flex gap-8">
              <div>
                <div className="text-xl font-semibold tabular-nums">{count(p.unresolved_issues)}</div>
                <div className="text-xs text-muted-foreground">Unresolved issues</div>
              </div>
              <div>
                <div className="text-xl font-semibold tabular-nums">{count(p.events_24h)}</div>
                <div className="text-xs text-muted-foreground">Events · 24h</div>
              </div>
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}

function NewProjectDialog() {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [slugEdited, setSlugEdited] = useState(false)
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: () => api.createProject(slug, name),
    onSuccess: () => {
      toast.success(`Project ${name || slug} created`)
      qc.invalidateQueries({ queryKey: ["projects"] })
      setOpen(false)
      setName("")
      setSlug("")
      setSlugEdited(false)
    },
  })
  const toSlug = (s: string) =>
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 63)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <PlusIcon /> New project
      </DialogTrigger>
      <DialogContent>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
          className="space-y-4"
        >
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>Usually one project per service.</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="project-name">Name</Label>
            <Input
              id="project-name"
              autoFocus
              value={name}
              placeholder="Library API"
              onChange={(e) => {
                setName(e.target.value)
                if (!slugEdited) setSlug(toSlug(e.target.value))
              }}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="project-slug">Slug</Label>
            <Input
              id="project-slug"
              value={slug}
              className="font-mono"
              placeholder="library-api"
              onChange={(e) => {
                setSlugEdited(true)
                setSlug(toSlug(e.target.value))
              }}
            />
          </div>
          {create.error && <p className="text-sm text-destructive">{create.error.message}</p>}
          <DialogFooter>
            <Button type="submit" disabled={!slug || create.isPending}>
              Create project
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function ProjectSettingsPage() {
  const { slug = "" } = useParams()
  const isAdmin = useMe().data?.is_admin
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects })
  const keys = useQuery({ queryKey: ["keys", slug], queryFn: () => api.keys(slug) })
  const adapters = useQuery({ queryKey: ["adapters"], queryFn: api.adapters })
  const meta = useQuery({ queryKey: ["meta"], queryFn: api.meta })
  const project = projects.data?.projects.find((p) => p.slug === slug)
  const active = keys.data?.keys.filter((k) => !k.revoked_at) ?? []
  const revoked = keys.data?.keys.filter((k) => k.revoked_at) ?? []
  // The setup guide appears for a project with no events and stays until
  // the page is left, so the first event's arrival can be shown in place.
  const first = useQuery({ queryKey: ["first-issue", slug], queryFn: () => api.issues({ project: slug, status: "all", limit: 1 }) })
  const [guide, setGuide] = useState(false)
  if (first.data?.total === 0 && !guide) setGuide(true)
  const newKey = isAdmin && adapters.data ? <NewKeyDialog slug={slug} adapters={adapters.data.adapters} /> : null

  return (
    <div className="space-y-8">
      <div className="space-y-3">
        <Breadcrumb>
          <BreadcrumbList>
            <BreadcrumbItem>
              <BreadcrumbLink render={<Link to="/projects" />}>Projects</BreadcrumbLink>
            </BreadcrumbItem>
            <BreadcrumbSeparator />
            <BreadcrumbItem>
              <BreadcrumbPage>{project?.name ?? slug}</BreadcrumbPage>
            </BreadcrumbItem>
          </BreadcrumbList>
        </Breadcrumb>
        <PageHeader
          title={project?.name ?? slug}
          description={
            <>
              Keys let an SDK report to this project{meta.data ? `; events are kept for ${meta.data.retention_days} days` : ""}.{" "}
              <Link to={`/issues?project=${slug}`} className="text-primary hover:underline">
                View issues
              </Link>
            </>
          }
          actions={!guide && newKey}
        />
      </div>

      {guide && (
        <GetStarted
          slug={slug}
          hasKey={active.length > 0}
          keyAction={newKey ?? <span className="text-sm text-muted-foreground">Ask an administrator to create a key.</span>}
        />
      )}

      {!(guide && active.length === 0) && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold">Active keys</h2>
          <div className="overflow-hidden rounded-xl border bg-card">
            {keys.isPending && <Skeleton className="h-24 w-full" />}
            {keys.data && active.length === 0 && (
              <EmptyState icon={<KeyRoundIcon />} title="No active keys">
                {isAdmin ? "Create a key for the SDK your service uses." : "Ask an administrator to create one."}
              </EmptyState>
            )}
            <ul className="divide-y">
              {active.map((k) => (
                <KeyRow key={k.id} slug={slug} k={k} canRevoke={!!isAdmin} />
              ))}
            </ul>
          </div>
        </section>
      )}

      {revoked.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold text-muted-foreground">Revoked</h2>
          <ul className="divide-y overflow-hidden rounded-xl border bg-card opacity-70">
            {revoked.map((k) => (
              <KeyRow key={k.id} slug={slug} k={k} canRevoke={false} />
            ))}
          </ul>
        </section>
      )}

      {project && <RepositorySection slug={slug} repoURL={project.repo_url} canEdit={!!isAdmin} />}

      {isAdmin && <AlertsSection slug={slug} />}

      {isAdmin && <SourceMapsSection slug={slug} />}

      {adapters.data && (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold">Supported SDKs</h2>
          <div className="grid gap-3 md:grid-cols-2">
            {adapters.data.adapters.map((a) => (
              <div key={a.name} className="rounded-xl border bg-card p-4">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{adapterLabel(a.name)}</span>
                  {a.enabled ? <Badge variant="outline">Enabled</Badge> : <Badge variant="secondary">Disabled on this server</Badge>}
                  {a.experimental && <Badge variant="secondary">Experimental</Badge>}
                </div>
                <p className="mt-1.5 text-sm text-muted-foreground">{a.summary}</p>
                <ul className="mt-3 space-y-0.5">
                  {a.tested_sdk.map((s) => (
                    <li key={s} className="font-mono text-xs text-muted-foreground">
                      {s}
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

function RepositorySection({ slug, repoURL, canEdit }: { slug: string; repoURL: string; canEdit: boolean }) {
  const [value, setValue] = useState(repoURL)
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: () => api.updateProject(slug, value),
    onSuccess: () => {
      toast.success(value.trim() ? "Releases now link to their commits" : "Commit links turned off")
      qc.invalidateQueries({ queryKey: ["projects"] })
    },
    onError: (e) => toast.error(e.message),
  })
  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-sm font-semibold">Repository</h2>
        <p className="text-sm text-muted-foreground">Releases that are commit SHAs link to the commit in this repository.</p>
      </div>
      {canEdit ? (
        <form
          className="flex max-w-xl gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate()
          }}
        >
          <div className="relative flex-1">
            <GitBranchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder="https://github.com/owner/repo" className="pl-8" aria-label="Repository URL" />
          </div>
          <Button type="submit" variant="outline" disabled={save.isPending || value.trim() === repoURL}>
            Save
          </Button>
        </form>
      ) : (
        <p className="font-mono text-sm">{repoURL || <span className="font-sans text-muted-foreground">Not set</span>}</p>
      )}
    </section>
  )
}

function KeyRow({ slug, k, canRevoke }: { slug: string; k: Key; canRevoke: boolean }) {
  const qc = useQueryClient()
  const revoke = useMutation({
    mutationFn: () => api.revokeKey(slug, k.id),
    onSuccess: () => {
      toast.success("Key revoked; SDKs using it are now rejected")
      qc.invalidateQueries({ queryKey: ["keys", slug] })
    },
    onError: (e) => toast.error(e.message),
  })
  const [confirm, setConfirm] = useState(false)
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3">
      <Badge variant="outline">{adapterLabel(k.adapter)}</Badge>
      <span className="min-w-0 flex-1 truncate text-sm font-medium">{k.label || <span className="text-muted-foreground">Unlabelled key</span>}</span>
      <span className="text-xs text-muted-foreground">
        {k.revoked_at ? (
          <>Revoked {ago(k.revoked_at)}</>
        ) : (
          <>
            Created <Time iso={k.created_at}>{ago(k.created_at)}</Time>
            {k.created_by && <> by {k.created_by}</>}
          </>
        )}
      </span>
      {canRevoke &&
        (confirm ? (
          <div className="flex gap-1">
            <Button size="sm" variant="destructive" onClick={() => revoke.mutate()} disabled={revoke.isPending}>
              Revoke
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>
              Cancel
            </Button>
          </div>
        ) : (
          <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => setConfirm(true)}>
            Revoke
          </Button>
        ))}
    </li>
  )
}

const sdkLabels: Record<string, string> = {
  sentry: "Sentry SDK (any language)",
  appsignal: "AppSignal (Elixir, Ruby, Node.js, Python)",
  "appsignal-frontend": "AppSignal browser SDK (@appsignal/javascript)",
}
const sdkLabel = (name: string) => sdkLabels[name] ?? adapterLabel(name)

function NewKeyDialog({ slug, adapters }: { slug: string; adapters: AdapterInfo[] }) {
  const [open, setOpen] = useState(false)
  const [adapter, setAdapter] = useState(adapters.find((a) => a.enabled)?.name ?? "sentry")
  const [label, setLabel] = useState("")
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: () => api.createKey(slug, adapter, label),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["keys", slug] }),
  })
  const reset = (o: boolean) => {
    setOpen(o)
    if (!o) {
      create.reset()
      setLabel("")
    }
  }
  const chosen = adapters.find((a) => a.name === adapter)
  return (
    <Dialog open={open} onOpenChange={reset}>
      <DialogTrigger render={<Button />}>
        <PlusIcon /> New key
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {create.data ? (
          <div className="space-y-4">
            <DialogHeader>
              <DialogTitle>Your new key</DialogTitle>
              <DialogDescription>Copy these settings into the service's environment.</DialogDescription>
            </DialogHeader>
            <Alert>
              <TriangleAlertIcon />
              <AlertTitle>Shown once</AlertTitle>
              <AlertDescription>Watchtower stores only a hash of this key and cannot show it again.</AlertDescription>
            </Alert>
            <div className="space-y-2">
              {Object.entries(create.data.settings).map(([k, v]) => (
                <div key={k} className="rounded-lg border bg-muted/40">
                  <div className="flex items-center justify-between px-3 pt-2 text-xs text-muted-foreground">
                    <span className="font-mono">{k}</span>
                    <CopyButton value={v} label={`Copy ${k}`} />
                  </div>
                  <code className="block px-3 pb-2.5 text-xs break-all">{v}</code>
                </div>
              ))}
            </div>
            {adapter === "appsignal" && (
              <p className="text-xs text-muted-foreground">
                Works for the Elixir, Ruby, Node.js and Python integrations. Nothing else changes: AppSignal packages and instrumentation stay as they are.
                Restart the service to apply.
              </p>
            )}
            {adapter === "appsignal-frontend" && (
              <p className="text-xs text-muted-foreground">
                Pass these to <code>new Appsignal({"{"} key, uri {"}"})</code> in the browser. This key is public by design; it can only send errors to this project.
              </p>
            )}
            {adapter === "sentry" && (
              <p className="text-xs text-muted-foreground">Use this DSN wherever the Sentry SDK is configured, in place of the sentry.io DSN.</p>
            )}
            <DialogFooter>
              <Button onClick={() => reset(false)}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              create.mutate()
            }}
            className="space-y-4"
          >
            <DialogHeader>
              <DialogTitle>New key</DialogTitle>
              <DialogDescription>Pick the SDK the service already uses.</DialogDescription>
            </DialogHeader>
            <div className="space-y-1.5">
              <Label>SDK</Label>
              <Select
                value={adapter}
                onValueChange={(v) => setAdapter(String(v))}
                items={adapters.map((a) => ({ value: a.name, label: sdkLabel(a.name) }))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {adapters.map((a) => (
                    <SelectItem key={a.name} value={a.name}>
                      {sdkLabel(a.name)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {chosen && !chosen.enabled && (
                <p className="text-xs text-level-warning">This adapter is disabled on the server; add it to WATCHTOWER_ADAPTERS first.</p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="key-label">Label</Label>
              <Input id="key-label" value={label} placeholder="library-api · production" onChange={(e) => setLabel(e.target.value)} />
            </div>
            {create.error && <p className="text-sm text-destructive">{create.error.message}</p>}
            <DialogFooter>
              <Button type="submit" disabled={create.isPending}>
                Create key
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
