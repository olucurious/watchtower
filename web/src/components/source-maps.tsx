import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { FileCode2Icon, PlusIcon, TriangleAlertIcon } from "lucide-react"

import { api, type UploadToken } from "@/lib/api"
import { ago } from "@/lib/format"
import { CopyButton, EmptyState, Time } from "@/components/bits"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"

function size(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export function SourceMapsSection({ slug }: { slug: string }) {
  const data = useQuery({ queryKey: ["sourcemaps", slug], queryFn: () => api.sourceMaps(slug) })
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: (id: number) => api.deleteBundle(slug, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sourcemaps", slug] }),
  })
  const tokens = data.data?.tokens.filter((t) => !t.revoked_at) ?? []
  return (
    <section className="space-y-3">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold">Source maps</h2>
          <p className="text-sm text-muted-foreground">
            Upload with <code className="text-xs">sentry-cli</code> or a Sentry bundler plugin; minified JavaScript stack traces are mapped back to your source.
          </p>
        </div>
        <NewTokenDialog slug={slug} />
      </div>
      {tokens.length > 0 && (
        <ul className="divide-y overflow-hidden rounded-xl border bg-card">
          {tokens.map((t) => (
            <TokenRow key={t.id} slug={slug} t={t} />
          ))}
        </ul>
      )}
      <div className="overflow-hidden rounded-xl border bg-card">
        {data.isPending && <Skeleton className="h-16 w-full" />}
        {data.data && data.data.bundles.length === 0 && (
          <EmptyState icon={<FileCode2Icon />} title="No source maps uploaded">
            Create an upload token, then run <code className="text-xs">sentry-cli sourcemaps inject</code> and <code className="text-xs">upload</code> in your build.
          </EmptyState>
        )}
        {data.data && data.data.bundles.length > 0 && (
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-[11px] tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-4 py-2 font-medium">Release</th>
                <th className="px-4 py-2 font-medium">Files</th>
                <th className="px-4 py-2 font-medium max-sm:hidden">Bundle</th>
                <th className="px-4 py-2 font-medium">Uploaded</th>
                <th />
              </tr>
            </thead>
            <tbody className="divide-y">
              {data.data.bundles.map((b) => (
                <tr key={b.id}>
                  <td className="px-4 py-2.5 font-mono text-xs">{b.release || <span className="text-muted-foreground">debug IDs only</span>}</td>
                  <td className="px-4 py-2.5 text-muted-foreground tabular-nums">
                    {b.file_count} · {size(b.size_bytes)}
                  </td>
                  <td className="px-4 py-2.5 font-mono text-xs text-muted-foreground max-sm:hidden">{b.bundle_id.slice(0, 8) || "—"}</td>
                  <td className="px-4 py-2.5 text-muted-foreground">
                    <Time iso={b.created_at}>{ago(b.created_at)}</Time>
                  </td>
                  <td className="px-2 text-right">
                    <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => remove.mutate(b.id)}>
                      Delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  )
}

function TokenRow({ slug, t }: { slug: string; t: UploadToken }) {
  const qc = useQueryClient()
  const revoke = useMutation({
    mutationFn: () => api.revokeUploadToken(slug, t.id),
    onSuccess: () => {
      toast.success("Upload token revoked")
      qc.invalidateQueries({ queryKey: ["sourcemaps", slug] })
    },
  })
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5 text-sm">
      <span className="font-medium">{t.name}</span>
      <span className="text-xs text-muted-foreground">{t.project ? "This project" : "All projects"}</span>
      <span className="ml-auto text-xs text-muted-foreground">{t.last_used_at ? `Last used ${ago(t.last_used_at)}` : "Never used"}</span>
      <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => revoke.mutate()}>
        Revoke
      </Button>
    </li>
  )
}

function NewTokenDialog({ slug }: { slug: string }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: () => api.createUploadToken(slug, name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sourcemaps", slug] }),
  })
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      create.reset()
      setName("")
    }
  }
  const env = create.data
    ? Object.entries(create.data.settings)
        .map(([k, v]) => `${k}=${v}`)
        .join("\n")
    : ""
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <PlusIcon /> Upload token
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        {create.data ? (
          <div className="space-y-4">
            <DialogHeader>
              <DialogTitle>Upload token created</DialogTitle>
              <DialogDescription>Add these to your CI environment, then run the commands after your build.</DialogDescription>
            </DialogHeader>
            <Alert>
              <TriangleAlertIcon />
              <AlertTitle>Shown once</AlertTitle>
              <AlertDescription>Watchtower stores only a hash of this token.</AlertDescription>
            </Alert>
            <CodeBlock label="Environment" text={env} />
            <CodeBlock label="After your build" text={"npx @sentry/cli sourcemaps inject ./dist\nnpx @sentry/cli sourcemaps upload ./dist"} />
            <p className="text-xs text-muted-foreground">
              Sentry's Vite, webpack and esbuild plugins work too: give them the same URL, token, org and project.
            </p>
            <DialogFooter>
              <Button onClick={() => close(false)}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <DialogHeader>
              <DialogTitle>New upload token</DialogTitle>
              <DialogDescription>Lets your build upload source maps to this project only.</DialogDescription>
            </DialogHeader>
            <div className="space-y-1.5">
              <Label htmlFor="token-name">Name</Label>
              <Input id="token-name" placeholder="GitLab CI · web" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            {create.error && <p className="text-sm text-destructive">{create.error.message}</p>}
            <DialogFooter>
              <Button type="submit" disabled={create.isPending}>
                Create token
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

function CodeBlock({ label, text }: { label: string; text: string }) {
  return (
    <div className="rounded-lg border bg-muted/40">
      <div className="flex items-center justify-between px-3 pt-2 text-xs text-muted-foreground">
        <span>{label}</span>
        <CopyButton value={text} label={`Copy ${label.toLowerCase()}`} />
      </div>
      <pre className="overflow-x-auto px-3 pb-2.5 text-xs">{text}</pre>
    </div>
  )
}
