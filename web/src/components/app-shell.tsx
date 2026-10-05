import { NavLink, Navigate, Outlet, useLocation, useNavigate } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useTheme } from "next-themes"
import { toast } from "sonner"
import { BugIcon, ChevronsUpDownIcon, FolderIcon, KeyRoundIcon, LogOutIcon, MonitorIcon, MoonIcon, SunIcon, UsersIcon } from "lucide-react"

import { api, ApiError } from "@/lib/api"
import { Logo } from "@/components/bits"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar"
import { Skeleton } from "@/components/ui/skeleton"

export function useMe() {
  return useQuery({ queryKey: ["me"], queryFn: api.me, retry: false, staleTime: 60_000 })
}

/** Requires a session; renders the sidebar layout around the page. */
export function AppShell() {
  const me = useMe()
  const location = useLocation()
  if (me.isPending)
    return (
      <div className="flex h-svh items-center justify-center">
        <Skeleton className="h-6 w-40" />
      </div>
    )
  if (me.error instanceof ApiError && me.error.status === 401)
    return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />
  if (me.error && !me.data) return <p role="alert" className="p-6 text-sm text-destructive">{me.error.message}</p>
  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset className="min-w-0">
        <header className="sticky top-0 z-10 flex h-12 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur md:hidden">
          <SidebarTrigger />
          <span className="text-sm font-semibold">Watchtower</span>
        </header>
        <main className="mx-auto w-full max-w-[1400px] px-4 py-6 md:px-8">
          <Outlet />
        </main>
      </SidebarInset>
    </SidebarProvider>
  )
}

const nav = [
  { to: "/issues", label: "Issues", icon: BugIcon },
  { to: "/projects", label: "Projects", icon: FolderIcon },
]

function AppSidebar() {
  const me = useMe().data
  const location = useLocation()
  const active = (to: string) => location.pathname === to || location.pathname.startsWith(to + "/")
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" render={<NavLink to="/issues" />} className="hover:bg-transparent">
              <div className="flex size-8 items-center justify-center rounded-md bg-sidebar-primary text-sidebar-primary-foreground">
                <Logo className="size-4.5" />
              </div>
              <span className="text-[15px] font-semibold tracking-tight text-sidebar-accent-foreground">Watchtower</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {nav.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton render={<NavLink to={item.to} />} isActive={active(item.to)} tooltip={item.label}>
                    <item.icon />
                    <span>{item.label}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/50">Settings</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {me?.is_admin && (
                <SidebarMenuItem>
                  <SidebarMenuButton render={<NavLink to="/settings/users" />} isActive={active("/settings/users")} tooltip="Members">
                    <UsersIcon />
                    <span>Members</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              )}
              <SidebarMenuItem>
                <SidebarMenuButton render={<NavLink to="/settings/account" />} isActive={active("/settings/account")} tooltip="Account">
                  <KeyRoundIcon />
                  <span>Account</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>{me && <UserMenu name={me.name || me.email} email={me.email} />}</SidebarFooter>
    </Sidebar>
  )
}

function UserMenu({ name, email }: { name: string; email: string }) {
  const { theme, setTheme } = useTheme()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: () => {
      qc.clear()
      navigate("/login", { replace: true })
    },
    onError: (error) => toast.error(error.message),
  })
  const initials = name
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]!.toUpperCase())
    .join("")
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger render={<SidebarMenuButton size="lg" className="data-popup-open:bg-sidebar-accent" />}>
            <Avatar className="size-8 rounded-md">
              <AvatarFallback className="rounded-md bg-sidebar-accent text-xs text-sidebar-accent-foreground">{initials}</AvatarFallback>
            </Avatar>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium text-sidebar-accent-foreground">{name}</span>
              <span className="truncate text-xs text-sidebar-foreground/60">{email}</span>
            </div>
            <ChevronsUpDownIcon className="ml-auto size-4 opacity-60" />
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-56">
            <DropdownMenuGroup>
              <DropdownMenuLabel>Theme</DropdownMenuLabel>
              {(
                [
                  ["light", "Light", SunIcon],
                  ["dark", "Dark", MoonIcon],
                  ["system", "System", MonitorIcon],
                ] as const
              ).map(([value, label, Icon]) => (
                <DropdownMenuItem key={value} onClick={() => setTheme(value)} className={theme === value ? "font-medium" : ""}>
                  <Icon />
                  {label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => logout.mutate()}>
              <LogOutIcon />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
