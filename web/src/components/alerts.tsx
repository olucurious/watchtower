import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { BellRingIcon, PencilIcon, PlusIcon, SendIcon, Trash2Icon, TriangleAlertIcon } from "lucide-react"

import { api, type AlertChannel, type AlertInput, type AlertKind, type LinearTeam, type Level } from "@/lib/api"
import { ago } from "@/lib/format"
import { EmptyState } from "@/components/bits"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

const levelLabels: Record<Level, string> = { fatal: "Fatal only", error: "Errors and above", warning: "Warnings and above", info: "Info and above", debug: "Everything" }

export function AlertsSection({ slug }: { slug: string }) {
  const alerts = useQuery({ queryKey: ["alerts", slug], queryFn: () => api.alerts(slug), refetchInterval: 5_000 })
  const envs = useQuery({ queryKey: ["environments", slug], queryFn: () => api.environments(slug) })
  const [editing, setEditing] = useState<AlertChannel | "new" | null>(null)
  const noKey = alerts.data && !alerts.data.secret_key_configured

  return (
    <section className="space-y-3">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold">Alerts</h2>
          <p className="text-sm text-muted-foreground">Post to Slack, or file issues in Linear, when an issue is new, comes back, or spikes.</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setEditing("new")} disabled={!!noKey}>
          <PlusIcon /> Add alert
        </Button>
      </div>
      {noKey && (
        <Alert>
          <TriangleAlertIcon />
          <AlertTitle>Set WATCHTOWER_SECRET_KEY to enable alerts</AlertTitle>
          <AlertDescription>
            Webhook URLs, bot tokens and Linear keys are encrypted with this key. Generate one with <code className="text-xs">openssl rand -hex 32</code>, add it to the server's
            environment and restart.
          </AlertDescription>
        </Alert>
      )}
      <div className="overflow-hidden rounded-xl border bg-card">
        {alerts.isPending && <Skeleton className="h-20 w-full" />}
        {alerts.data && alerts.data.channels.length === 0 && (
          <EmptyState icon={<BellRingIcon />} title="No alerts yet">
            Post new issues and regressions to a Slack channel, or connect Linear to file issues from here.
          </EmptyState>
        )}
        <ul className="divide-y">
          {alerts.data?.channels.map((c) => <ChannelRow key={c.id} slug={slug} c={c} onEdit={() => setEditing(c)} />)}
        </ul>
      </div>
      {editing && (
        <AlertDialog
          slug={slug}
          channel={editing === "new" ? null : editing}
          environments={envs.data?.environments ?? []}
          onClose={() => setEditing(null)}
        />
      )}
    </section>
  )
}

function ChannelRow({ slug, c, onEdit }: { slug: string; c: AlertChannel; onEdit: () => void }) {
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState(false)
  const test = useMutation({
    mutationFn: () => api.testAlert(slug, c.id),
    onSuccess: () => toast.success("Test alert queued; it should arrive within a few seconds"),
    onError: (e) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: () => api.deleteAlert(slug, c.id),
    onSuccess: () => {
      toast.success(`Removed ${c.name}`)
      qc.invalidateQueries({ queryKey: ["alerts", slug] })
    },
  })
  const triggers = [
    c.on_new_issue && "New issues",
    c.on_regression && "Regressions",
    c.frequency_threshold && `${c.frequency_threshold}+ events/hour`,
  ].filter(Boolean) as string[]
  if (triggers.length === 0) triggers.push("Manual only")
  return (
    <li className="flex flex-wrap items-start gap-x-4 gap-y-2 px-4 py-3.5">
      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{c.name}</span>
          <span className="font-mono text-xs text-muted-foreground">
            {c.kind === "slack_bot"
              ? `bot ${c.target_hint} → ${c.target_channel}`
              : c.kind === "linear"
                ? `Linear · ${c.target_label} · ${c.target_hint}`
                : c.target_hint}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {triggers.map((t) => (
            <Badge key={t} variant="secondary">
              {t}
            </Badge>
          ))}
          <span className="text-xs text-muted-foreground">
            {levelLabels[c.min_level]} · {c.environment || "all environments"}
          </span>
        </div>
        <p className="text-xs">
          {c.last_error ? (
            <span className="text-destructive">Last delivery failed: {c.last_error}</span>
          ) : c.last_delivery_at ? (
            <span className="text-muted-foreground">Last delivered {ago(c.last_delivery_at)}</span>
          ) : (
            <span className="text-muted-foreground">Nothing sent yet</span>
          )}
        </p>
      </div>
      <div className="flex items-center gap-1">
        <Button size="sm" variant="ghost" onClick={() => test.mutate()} disabled={test.isPending}>
          <SendIcon /> Test
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label={`Edit ${c.name}`} onClick={onEdit}>
          <PencilIcon />
        </Button>
        {confirm ? (
          <>
            <Button size="sm" variant="destructive" onClick={() => remove.mutate()}>
              Remove
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <Button size="icon-sm" variant="ghost" aria-label={`Remove ${c.name}`} onClick={() => setConfirm(true)}>
            <Trash2Icon />
          </Button>
        )}
      </div>
    </li>
  )
}

function AlertDialog({ slug, channel, environments, onClose }: { slug: string; channel: AlertChannel | null; environments: string[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [form, setForm] = useState<AlertInput>({
    name: channel?.name ?? "",
    kind: channel?.kind ?? "slack",
    webhook_url: "",
    bot_token: "",
    api_key: "",
    team_id: "", // set only when a team is picked, so saving rules alone needs no call to Linear
    channel: channel?.target_channel ?? "",
    on_new_issue: channel?.on_new_issue ?? true,
    on_regression: channel?.on_regression ?? true,
    frequency_threshold: channel?.frequency_threshold ?? null,
    min_level: channel?.min_level ?? "error",
    environment: channel?.environment ?? "",
  })
  const [spike, setSpike] = useState(channel?.frequency_threshold != null)
  const save = useMutation({
    mutationFn: () => {
      const body = { ...form, frequency_threshold: spike ? (form.frequency_threshold ?? 100) : null }
      if (!body.webhook_url) delete body.webhook_url
      if (!body.bot_token) delete body.bot_token
      if (!body.api_key) delete body.api_key
      if (body.kind !== "slack_bot") delete body.channel
      if (body.kind !== "linear") delete body.team_id
      return channel ? api.updateAlert(slug, channel.id, body) : api.createAlert(slug, body).then(() => undefined)
    },
    onSuccess: () => {
      toast.success(channel ? "Alert updated" : "Alert added. Send a test to check it")
      qc.invalidateQueries({ queryKey: ["alerts", slug] })
      onClose()
    },
  })
  const envItems = [{ value: "__all", label: "All environments" }, ...environments.map((e) => ({ value: e, label: e }))]
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>{channel ? "Edit alert" : "Add alert"}</DialogTitle>
            <DialogDescription>
              {form.kind === "linear"
                ? "Use a Linear API key (Settings › Security & access › Personal API keys). Watchtower files issues in the team you pick."
                : form.kind === "slack_bot"
                  ? "Use a Slack app's bot token (xoxb-…). Invite the bot to the channel, then give its channel ID."
                  : "Create an incoming webhook in Slack (Apps › Incoming Webhooks) for the channel, then paste its URL here."}
            </DialogDescription>
          </DialogHeader>
          {!channel && (
            <div className="space-y-1.5">
              <Label>Destination</Label>
              <ToggleGroup
                variant="outline"
                spacing={0}
                value={[form.kind === "linear" ? "linear" : "slack"]}
                onValueChange={(v) =>
                  v[0] &&
                  setForm({
                    ...form,
                    kind: v[0] as AlertKind,
                    // Linear issues are usually filed by hand; start with no automatic triggers.
                    ...(v[0] === "linear" ? { on_new_issue: false, on_regression: false } : { on_new_issue: true, on_regression: true }),
                  })
                }
              >
                <ToggleGroupItem value="slack" className="px-3 text-xs">
                  Slack
                </ToggleGroupItem>
                <ToggleGroupItem value="linear" className="px-3 text-xs">
                  Linear
                </ToggleGroupItem>
              </ToggleGroup>
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="alert-name">Name</Label>
            <Input
              id="alert-name"
              required
              placeholder={form.kind === "linear" ? "Storefront triage" : "#errors-web"}
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          {!channel && form.kind !== "linear" && (
            <div className="space-y-1.5">
              <Label>Connect with</Label>
              <ToggleGroup
                variant="outline"
                spacing={0}
                value={[form.kind ?? "slack"]}
                onValueChange={(v) => v[0] && setForm({ ...form, kind: v[0] as "slack" | "slack_bot" })}
              >
                <ToggleGroupItem value="slack" className="px-3 text-xs">
                  Incoming webhook
                </ToggleGroupItem>
                <ToggleGroupItem value="slack_bot" className="px-3 text-xs">
                  Bot token
                </ToggleGroupItem>
              </ToggleGroup>
            </div>
          )}
          {form.kind === "linear" ? (
            <LinearFields slug={slug} channel={channel} form={form} setForm={setForm} />
          ) : form.kind === "slack_bot" ? (
            <div className="grid gap-3 sm:grid-cols-[1fr_9rem]">
              <div className="space-y-1.5">
                <Label htmlFor="alert-token">Bot token</Label>
                <Input
                  id="alert-token"
                  type="password"
                  required={!channel}
                  autoComplete="off"
                  placeholder={channel ? `Keep current (${channel.target_hint})` : "xoxb-…"}
                  value={form.bot_token}
                  onChange={(e) => setForm({ ...form, bot_token: e.target.value })}
                  className="font-mono text-xs"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="alert-channel">Channel ID</Label>
                <Input
                  id="alert-channel"
                  required
                  placeholder="C0123ABCD"
                  value={form.channel}
                  onChange={(e) => setForm({ ...form, channel: e.target.value.trim() })}
                  className="font-mono text-xs"
                />
              </div>
              <p className="text-xs text-muted-foreground sm:col-span-2">The token is stored encrypted. Only its last characters are shown again.</p>
            </div>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="alert-webhook">Webhook URL</Label>
              <Input
                id="alert-webhook"
                type="url"
                required={!channel}
                autoComplete="off"
                placeholder={channel ? `Keep current (${channel.target_hint})` : "https://hooks.slack.com/services/…"}
                value={form.webhook_url}
                onChange={(e) => setForm({ ...form, webhook_url: e.target.value })}
                className="font-mono text-xs"
              />
              <p className="text-xs text-muted-foreground">Stored encrypted. Only the end of it is shown again.</p>
            </div>
          )}
          <fieldset className="space-y-2.5">
            <legend className="mb-2 text-sm font-medium">{form.kind === "linear" ? "Create a Linear issue when" : "Send when"}</legend>
            <Label className="flex items-center gap-2 font-normal">
              <Checkbox checked={form.on_new_issue} onCheckedChange={(c) => setForm({ ...form, on_new_issue: !!c })} />
              A new issue appears
            </Label>
            <Label className="flex items-center gap-2 font-normal">
              <Checkbox checked={form.on_regression} onCheckedChange={(c) => setForm({ ...form, on_regression: !!c })} />
              A resolved issue comes back
            </Label>
            <div className="flex flex-wrap items-center gap-2">
              <Label className="flex items-center gap-2 font-normal">
                <Checkbox checked={spike} onCheckedChange={(c) => setSpike(!!c)} />
                An issue reaches
              </Label>
              <Input
                aria-label="Events per hour"
                type="number"
                min={1}
                disabled={!spike}
                className="h-7 w-24"
                value={form.frequency_threshold ?? 100}
                onChange={(e) => setForm({ ...form, frequency_threshold: Number(e.target.value) })}
              />
              <span className="text-sm">events in an hour</span>
            </div>
            {form.kind === "linear" && (
              <p className="text-xs text-muted-foreground">
                Optional: anyone can also file an issue from its page. An issue already in Linear gets a comment instead of a duplicate, and completing it
                in Linear resolves it here.
              </p>
            )}
          </fieldset>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label>Minimum level</Label>
              <Select value={form.min_level} onValueChange={(v) => setForm({ ...form, min_level: v as Level })} items={levelLabels}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {Object.entries(levelLabels).map(([v, l]) => (
                    <SelectItem key={v} value={v}>
                      {l}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Environment</Label>
              <Select
                value={form.environment || "__all"}
                onValueChange={(v) => setForm({ ...form, environment: v === "__all" ? "" : String(v) })}
                items={envItems}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {envItems.map((e) => (
                    <SelectItem key={e.value} value={e.value}>
                      {e.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {save.error && <p className="text-sm text-destructive">{save.error.message}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending || (form.kind === "linear" && !channel && !form.team_id)}>
              {channel ? "Save" : "Add alert"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** API key and team for a Linear destination. Teams load once the key works. */
function LinearFields({
  slug,
  channel,
  form,
  setForm,
}: {
  slug: string
  channel: AlertChannel | null
  form: AlertInput
  setForm: (f: AlertInput) => void
}) {
  const [teams, setTeams] = useState<LinearTeam[] | null>(null)
  const load = useMutation({
    mutationFn: () => api.linearTeams(slug, form.api_key ? { api_key: form.api_key } : { channel_id: channel?.id }),
    onSuccess: (r) => {
      setTeams(r.teams)
      const current = channel?.target_channel
      setForm({ ...form, team_id: r.teams.some((t) => t.id === current) ? current! : (r.teams[0]?.id ?? "") })
    },
  })
  const teamItems = (teams ?? []).map((t) => ({ value: t.id, label: `${t.name} (${t.key})` }))
  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <Label htmlFor="linear-key">API key</Label>
        <div className="flex gap-2">
          <Input
            id="linear-key"
            type="password"
            autoComplete="off"
            required={!channel}
            placeholder={channel ? `Keep current (${channel.target_hint})` : "lin_api_…"}
            value={form.api_key}
            onChange={(e) => {
              setTeams(null)
              setForm({ ...form, api_key: e.target.value.trim() })
            }}
            className="font-mono text-xs"
          />
          <Button type="button" variant="outline" onClick={() => load.mutate()} disabled={load.isPending || (!form.api_key && !channel)}>
            {channel && !form.api_key ? "Change team" : "Load teams"}
          </Button>
        </div>
        {load.error ? (
          <p className="text-xs text-destructive">{load.error.message}</p>
        ) : (
          <p className="text-xs text-muted-foreground">Stored encrypted. Only its last characters are shown again.</p>
        )}
      </div>
      {teams ? (
        <div className="space-y-1.5">
          <Label>Team</Label>
          <Select value={form.team_id} onValueChange={(v) => setForm({ ...form, team_id: String(v) })} items={teamItems}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {teamItems.map((t) => (
                <SelectItem key={t.value} value={t.value}>
                  {t.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      ) : (
        channel && <p className="text-sm">Team: {channel.target_label}</p>
      )}
    </div>
  )
}
