import { useState } from "react"
import { Navigate, useNavigate } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { BotIcon, MailIcon, PlusIcon, TriangleAlertIcon, UserPlusIcon } from "lucide-react"

import { api, type AccessToken, type EmailPrefs } from "@/lib/api"
import { ago } from "@/lib/format"
import { CopyButton, PageHeader, Time } from "@/components/bits"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { useMe } from "@/components/app-shell"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

const MIN_PASSWORD = 12

export function MembersPage() {
  const me = useMe().data
  const users = useQuery({ queryKey: ["users"], queryFn: api.users, enabled: !!me?.is_admin })
  const qc = useQueryClient()
  const toggle = useMutation({
    mutationFn: ({ id, disabled }: { id: number; disabled: boolean }) => api.setUserDisabled(id, disabled),
    onSuccess: (_, v) => {
      toast.success(v.disabled ? "Account disabled and signed out" : "Account enabled")
      qc.invalidateQueries({ queryKey: ["users"] })
    },
    onError: (e) => toast.error(e.message),
  })
  if (me && !me.is_admin) return <Navigate to="/issues" replace />
  return (
    <div className="space-y-6">
      <PageHeader title="Members" description="People who can sign in to Watchtower." actions={<NewMemberDialog />} />
      <div className="overflow-hidden rounded-xl border bg-card">
        {users.isPending && <Skeleton className="h-32 w-full" />}
        <ul className="divide-y">
          {users.data?.users.map((u) => (
            <li key={u.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">{u.name || u.email}</span>
                  {u.is_admin && <Badge variant="secondary">Admin</Badge>}
                  {u.disabled && <Badge variant="outline">Disabled</Badge>}
                  {u.id === me?.id && <span className="text-xs text-muted-foreground">(you)</span>}
                </div>
                {u.name && <p className="truncate text-xs text-muted-foreground">{u.email}</p>}
              </div>
              <span className="text-xs text-muted-foreground">Joined {ago(u.created_at)}</span>
              {u.id !== me?.id && (
                <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => toggle.mutate({ id: u.id, disabled: !u.disabled })}>
                  {u.disabled ? "Enable" : "Disable"}
                </Button>
              )}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function NewMemberDialog() {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ email: "", name: "", password: "", is_admin: false })
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: () => api.createUser(form),
    onSuccess: (u) => {
      toast.success(`${u.email} can now sign in`)
      qc.invalidateQueries({ queryKey: ["users"] })
      setOpen(false)
      setForm({ email: "", name: "", password: "", is_admin: false })
    },
  })
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <UserPlusIcon /> Add member
      </DialogTrigger>
      <DialogContent>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>Add member</DialogTitle>
            <DialogDescription>Share the initial password privately; they can change it under Account.</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="m-email">Email</Label>
            <Input id="m-email" type="email" required autoFocus value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="m-name">Name</Label>
            <Input id="m-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="m-password">Initial password</Label>
            <Input
              id="m-password"
              type="password"
              autoComplete="new-password"
              required
              minLength={MIN_PASSWORD}
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
            />
            <p className="text-xs text-muted-foreground">At least {MIN_PASSWORD} characters.</p>
          </div>
          <Label className="flex items-center gap-2 font-normal">
            <Checkbox checked={form.is_admin} onCheckedChange={(c) => setForm({ ...form, is_admin: !!c })} />
            Administrator (manages projects, keys and members)
          </Label>
          {create.error && <p className="text-sm text-destructive">{create.error.message}</p>}
          <DialogFooter>
            <Button type="submit" disabled={create.isPending}>
              Add member
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function AccountPage() {
  const me = useMe().data
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const change = useMutation({
    mutationFn: () => api.changePassword(current, next),
    onSuccess: () => {
      toast.success("Password changed. Sign in again with the new password.")
      qc.clear()
      navigate("/login", { replace: true })
    },
  })
  return (
    <div className="max-w-xl space-y-6">
      <PageHeader title="Account" description={me?.email} />
      <EmailSettings />
      <AgentAccess />
      <form
        className="space-y-4 rounded-xl border bg-card p-6"
        onSubmit={(e) => {
          e.preventDefault()
          change.mutate()
        }}
      >
        <div>
          <h2 className="text-sm font-semibold">Change password</h2>
          <p className="text-sm text-muted-foreground">Signs you out everywhere.</p>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="current">Current password</Label>
          <Input id="current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="new">New password</Label>
          <Input id="new" type="password" autoComplete="new-password" required minLength={MIN_PASSWORD} value={next} onChange={(e) => setNext(e.target.value)} />
          <p className="text-xs text-muted-foreground">At least {MIN_PASSWORD} characters.</p>
        </div>
        {change.error && <p className="text-sm text-destructive">{change.error.message}</p>}
        <Button type="submit" disabled={change.isPending}>
          Change password
        </Button>
      </form>
    </div>
  )
}

const emailOptions: [keyof Omit<EmailPrefs, "digest">, string, string][] = [
  ["assigned", "Assigned to me", "Someone else assigns you an issue."],
  ["regressed", "My issues regress", "An issue you own comes back after being resolved."],
  ["comments", "Comments on my issues", "Someone comments on an issue you own."],
]

function EmailSettings() {
  const meta = useQuery({ queryKey: ["meta"], queryFn: api.meta })
  const prefs = useQuery({ queryKey: ["email-prefs"], queryFn: api.emailPrefs })
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: api.setEmailPrefs,
    onMutate: (p) => qc.setQueryData(["email-prefs"], p),
    onSuccess: () => toast.success("Email settings saved"),
    onError: (e) => {
      toast.error(e.message)
      qc.invalidateQueries({ queryKey: ["email-prefs"] })
    },
  })
  const test = useMutation({
    mutationFn: api.sendTestEmail,
    onSuccess: (r) => toast.success(`Test email sent to ${r.sent_to}`),
    onError: (e) => toast.error(e.message),
  })
  const enabled = meta.data?.email_enabled
  const p = prefs.data
  return (
    <section className="space-y-5 rounded-xl border bg-card p-6">
      <div>
        <h2 className="text-sm font-semibold">Email notifications</h2>
        <p className="text-sm text-muted-foreground">Sent to your account address. Channel alerts are set per project.</p>
      </div>
      {meta.data && !enabled && (
        <p className="rounded-lg bg-muted/60 px-3 py-2 text-sm text-muted-foreground">
          Email is not set up on this server yet. An administrator can configure a provider (any SMTP service, or Cloudflare Email Service) with{" "}
          <code className="text-xs">WATCHTOWER_EMAIL_PROVIDER</code>; these settings take effect once it is.
        </p>
      )}
      {!p ? (
        <Skeleton className="h-32 w-full" />
      ) : (
        <>
          <div className="space-y-3">
            {emailOptions.map(([key, label, hint]) => (
              <label key={key} className="flex cursor-pointer items-start gap-3">
                <Checkbox className="mt-0.5" checked={p[key]} onCheckedChange={(c) => save.mutate({ ...p, [key]: !!c })} />
                <span>
                  <span className="block text-sm font-medium">{label}</span>
                  <span className="block text-xs text-muted-foreground">{hint}</span>
                </span>
              </label>
            ))}
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
            <div>
              <span className="block text-sm font-medium">Digest</span>
              <span className="block text-xs text-muted-foreground">New issues, regressions and the busiest issues, across all projects.</span>
            </div>
            <ToggleGroup
              variant="outline"
              size="sm"
              spacing={0}
              value={[p.digest]}
              onValueChange={(v) => v[0] && save.mutate({ ...p, digest: v[0] as EmailPrefs["digest"] })}
            >
              <ToggleGroupItem value="off" className="px-3 text-xs">
                Off
              </ToggleGroupItem>
              <ToggleGroupItem value="daily" className="px-3 text-xs">
                Daily
              </ToggleGroupItem>
              <ToggleGroupItem value="weekly" className="px-3 text-xs">
                Weekly
              </ToggleGroupItem>
            </ToggleGroup>
          </div>
        </>
      )}
      {enabled && (
        <Button variant="outline" size="sm" onClick={() => test.mutate()} disabled={test.isPending}>
          <MailIcon /> {test.isPending ? "Sending…" : "Send test email"}
        </Button>
      )}
    </section>
  )
}

const expiryOptions = { "30": "30 days", "90": "90 days", "0": "No expiry" }

/** Personal access tokens for coding agents, through Watchtower's MCP server. */
function AgentAccess() {
  const tokens = useQuery({ queryKey: ["access-tokens"], queryFn: api.accessTokens })
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const revoke = useMutation({
    mutationFn: (id: number) => api.revokeAccessToken(id),
    onSuccess: () => {
      toast.success("Token revoked")
      qc.invalidateQueries({ queryKey: ["access-tokens"] })
    },
    onError: (e) => toast.error(e.message),
  })
  // Expiry is judged at the time the list was fetched, which keeps render pure.
  const active =
    tokens.data?.tokens.filter((t) => !t.revoked_at && !(t.expires_at && new Date(t.expires_at).getTime() < tokens.dataUpdatedAt)) ?? []
  return (
    <section className="space-y-4 rounded-xl border bg-card p-6">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold">Agent access</h2>
          <p className="text-sm text-muted-foreground">
            Let a coding agent (Claude Code, Codex, Cursor…) read issues and, with a write token, assign, comment and resolve them as you, through Watchtower's MCP
            server.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
          <PlusIcon /> New token
        </Button>
      </div>
      {tokens.isPending ? (
        <Skeleton className="h-16 w-full" />
      ) : active.length === 0 ? (
        <p className="text-sm text-muted-foreground">No tokens yet.</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {active.map((t) => (
            <TokenRow key={t.id} t={t} onRevoke={() => revoke.mutate(t.id)} />
          ))}
        </ul>
      )}
      {open && tokens.data && <NewTokenDialog mcpURL={tokens.data.mcp_url} onClose={() => setOpen(false)} />}
    </section>
  )
}

function TokenRow({ t, onRevoke }: { t: AccessToken; onRevoke: () => void }) {
  const [confirm, setConfirm] = useState(false)
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 text-sm">
      <BotIcon className="size-4 text-muted-foreground" />
      <span className="font-medium">{t.name}</span>
      <Badge variant={t.scope === "write" ? "default" : "secondary"}>{t.scope === "write" ? "Read and write" : "Read only"}</Badge>
      <span className="font-mono text-xs text-muted-foreground">{t.hint}</span>
      <span className="ml-auto text-xs text-muted-foreground">
        {t.last_used_at ? <>Used <Time iso={t.last_used_at}>{ago(t.last_used_at)}</Time></> : "Never used"}
        {" · "}
        {t.expires_at ? <>Expires <Time iso={t.expires_at}>{ago(t.expires_at)}</Time></> : "No expiry"}
      </span>
      {confirm ? (
        <span className="flex gap-1">
          <Button size="sm" variant="destructive" onClick={onRevoke}>
            Revoke
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>
            Cancel
          </Button>
        </span>
      ) : (
        <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={() => setConfirm(true)}>
          Revoke
        </Button>
      )}
    </li>
  )
}

function NewTokenDialog({ mcpURL, onClose }: { mcpURL: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState("")
  const [scope, setScope] = useState<"read" | "write">("write")
  const [expiry, setExpiry] = useState("90")
  const create = useMutation({
    mutationFn: () => api.createAccessToken({ name, scope, expires_in_days: Number(expiry) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["access-tokens"] }),
  })
  const token = create.data?.token
  const setups = token
    ? [
        { label: "Claude Code", code: `claude mcp add --transport http watchtower ${mcpURL} --header "Authorization: Bearer ${token}"` },
        {
          label: "Codex (~/.codex/config.toml; export WATCHTOWER_TOKEN=…)",
          code: `[mcp_servers.watchtower]\nurl = "${mcpURL}"\nbearer_token_env_var = "WATCHTOWER_TOKEN"`,
        },
        {
          label: "Cursor and other clients (mcp.json)",
          code: JSON.stringify({ mcpServers: { watchtower: { url: mcpURL, headers: { Authorization: `Bearer ${token}` } } } }, null, 2),
        },
      ]
    : []
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
        {token ? (
          <div className="min-w-0 space-y-4">
            <DialogHeader>
              <DialogTitle>Your new token</DialogTitle>
              <DialogDescription>Add Watchtower to your agent with one of these.</DialogDescription>
            </DialogHeader>
            <Alert>
              <TriangleAlertIcon />
              <AlertTitle>Shown once</AlertTitle>
              <AlertDescription>Watchtower stores only a hash of this token. Treat it like a password: it acts as you.</AlertDescription>
            </Alert>
            <div className="min-w-0 rounded-lg border bg-muted/40">
              <div className="flex items-center justify-between px-3 pt-2 text-xs text-muted-foreground">
                <span>Token</span>
                <CopyButton value={token} label="Copy token" />
              </div>
              <code className="block px-3 pb-2.5 text-xs break-all">{token}</code>
            </div>
            {setups.map((s) => (
              <div key={s.label} className="min-w-0 rounded-lg border bg-muted/40">
                <div className="flex items-center justify-between px-3 pt-2 text-xs text-muted-foreground">
                  <span>{s.label}</span>
                  <CopyButton value={s.code} label={`Copy ${s.label} setup`} />
                </div>
                <pre className="overflow-x-auto px-3 pb-2.5 text-xs">
                  <code>{s.code}</code>
                </pre>
              </div>
            ))}
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="min-w-0 space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              create.mutate()
            }}
          >
            <DialogHeader>
              <DialogTitle>New agent token</DialogTitle>
              <DialogDescription>The token acts as you. Anything it changes is recorded under your name.</DialogDescription>
            </DialogHeader>
            <div className="space-y-1.5">
              <Label htmlFor="token-name">Name</Label>
              <Input id="token-name" required maxLength={80} placeholder="Claude Code on my laptop" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>Access</Label>
              <ToggleGroup variant="outline" spacing={0} value={[scope]} onValueChange={(v) => v[0] && setScope(v[0] as "read" | "write")}>
                <ToggleGroupItem value="write" className="px-3 text-xs">
                  Read and write
                </ToggleGroupItem>
                <ToggleGroupItem value="read" className="px-3 text-xs">
                  Read only
                </ToggleGroupItem>
              </ToggleGroup>
              <p className="text-xs text-muted-foreground">
                {scope === "write" ? "Can also assign, comment, resolve and file issues in Linear." : "Can find and read issues, but not change them."}
              </p>
            </div>
            <div className="space-y-1.5">
              <Label>Expires after</Label>
              <ToggleGroup variant="outline" spacing={0} value={[expiry]} onValueChange={(v) => v[0] && setExpiry(v[0])}>
                {Object.entries(expiryOptions).map(([v, l]) => (
                  <ToggleGroupItem key={v} value={v} className="px-3 text-xs">
                    {l}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </div>
            {create.error && <p className="text-sm text-destructive">{create.error.message}</p>}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || !name.trim()}>
                Create token
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
