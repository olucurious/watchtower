import { lazy, StrictMode, Suspense } from "react"
import { createRoot } from "react-dom/client"
import { createBrowserRouter, Navigate, RouterProvider } from "react-router"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ThemeProvider } from "next-themes"

import "./index.css"
import { ApiError } from "@/lib/api"
import { AppShell } from "@/components/app-shell"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { IssuesPage } from "@/pages/issues"
import { LoginPage } from "@/pages/login"
import { ProjectSettingsPage, ProjectsPage } from "@/pages/projects"
import { AccountPage, MembersPage } from "@/pages/settings"

// The issue page carries the charting library; load it on demand.
const IssuePage = lazy(() => import("@/pages/issue").then((m) => ({ default: m.IssuePage })))
const issuePage = (
  <Suspense>
    <IssuePage />
  </Suspense>
)

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: (n, err) => !(err instanceof ApiError && err.status < 500) && n < 2,
    },
  },
})

const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    element: <AppShell />,
    children: [
      { index: true, element: <Navigate to="/issues" replace /> },
      { path: "issues", element: <IssuesPage /> },
      { path: "issues/:id", element: issuePage },
      { path: "issues/:id/events/:eventId", element: issuePage },
      { path: "projects", element: <ProjectsPage /> },
      { path: "projects/:slug", element: <ProjectSettingsPage /> },
      { path: "settings/users", element: <MembersPage /> },
      { path: "settings/account", element: <AccountPage /> },
      { path: "*", element: <Navigate to="/issues" replace /> },
    ],
  },
])

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delay={300}>
          <RouterProvider router={router} />
          <Toaster position="bottom-right" />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
)
