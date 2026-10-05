import { useState } from "react"
import { Navigate, useNavigate, useSearchParams } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2Icon } from "lucide-react"

import { api } from "@/lib/api"
import { Logo } from "@/components/bits"
import { useMe } from "@/components/app-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function LoginPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const me = useMe()
  const meta = useQuery({ queryKey: ["meta"], queryFn: api.meta })
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const next = params.get("next")?.startsWith("/") ? params.get("next")! : "/issues"
  const login = useMutation({
    mutationFn: () => api.login(email, password),
    onSuccess: (user) => {
      qc.setQueryData(["me"], user)
      navigate(next, { replace: true })
    },
  })
  if (me.data) return <Navigate to={next} replace />
  if (meta.data && !meta.data.has_users) return <SetupPage version={meta.data.version} />

  return (
    <div className="flex min-h-svh flex-col items-center justify-center bg-muted/40 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <div className="flex size-11 items-center justify-center rounded-xl bg-sidebar text-sidebar-primary shadow-sm">
            <Logo className="size-6" />
          </div>
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Sign in to Watchtower</h1>
            <p className="mt-1 text-sm text-muted-foreground">Errors from every SDK, in one place.</p>
          </div>
        </div>
        <form
          className="space-y-4 rounded-xl border bg-card p-6 shadow-xs"
          onSubmit={(e) => {
            e.preventDefault()
            login.mutate()
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="email">Email</Label>
            <Input id="email" type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="password">Password</Label>
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {login.error && <p className="text-sm text-destructive">{login.error.message}</p>}
          <Button type="submit" className="h-9 w-full" disabled={login.isPending}>
            {login.isPending && <Loader2Icon className="animate-spin" />}
            Sign in
          </Button>
        </form>
        {meta.data && <p className="mt-6 text-center text-xs text-muted-foreground">Watchtower {meta.data.version}</p>}
      </div>
    </div>
  )
}

/** First run: create the administrator with the code the server printed. */
function SetupPage({ version }: { version: string }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [form, setForm] = useState({ code: "", name: "", email: "", password: "" })
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [k]: e.target.value })
  const setup = useMutation({
    mutationFn: () => api.setup(form),
    onSuccess: (user) => {
      qc.setQueryData(["me"], user)
      qc.invalidateQueries({ queryKey: ["meta"] })
      navigate("/projects", { replace: true })
    },
  })
  return (
    <div className="flex min-h-svh flex-col items-center justify-center bg-muted/40 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <div className="flex size-11 items-center justify-center rounded-xl bg-sidebar text-sidebar-primary shadow-sm">
            <Logo className="size-6" />
          </div>
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Welcome to Watchtower</h1>
            <p className="mt-1 text-sm text-muted-foreground">Create the administrator account to get started.</p>
          </div>
        </div>
        <form
          className="space-y-4 rounded-xl border bg-card p-6 shadow-xs"
          onSubmit={(e) => {
            e.preventDefault()
            setup.mutate()
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="code">Setup code</Label>
            <Input
              id="code"
              autoFocus
              required
              autoComplete="off"
              spellCheck={false}
              placeholder="XXXX-XXXX-XXXX"
              className="font-mono uppercase"
              value={form.code}
              onChange={set("code")}
            />
            <p className="text-xs text-muted-foreground">
              The installer printed it. To print a new one, run{" "}
              <code className="rounded bg-muted px-1 py-0.5">docker compose exec watchtower watchtower setup-code</code> where Watchtower is
              installed.
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input id="name" autoComplete="name" value={form.name} onChange={set("name")} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="setup-email">Email</Label>
            <Input id="setup-email" type="email" autoComplete="username" required value={form.email} onChange={set("email")} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="setup-password">Password</Label>
            <Input id="setup-password" type="password" autoComplete="new-password" required minLength={12} value={form.password} onChange={set("password")} />
            <p className="text-xs text-muted-foreground">At least 12 characters.</p>
          </div>
          {setup.error && <p className="text-sm text-destructive">{setup.error.message}</p>}
          <Button type="submit" className="h-9 w-full" disabled={setup.isPending}>
            {setup.isPending && <Loader2Icon className="animate-spin" />}
            Create account
          </Button>
        </form>
        <p className="mt-6 text-center text-xs text-muted-foreground">Watchtower {version}</p>
      </div>
    </div>
  )
}
