import { format, formatDistanceToNowStrict } from "date-fns"

export function ago(iso: string | null | undefined): string {
  if (!iso) return "—"
  return formatDistanceToNowStrict(new Date(iso), { addSuffix: true })
}

/** Compact relative age for dense tables: "4m", "3h", "2d". */
export function shortAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return `${Math.floor(s)}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d`
  if (s < 86400 * 365) return `${Math.floor(s / (86400 * 30))}mo`
  return `${Math.floor(s / (86400 * 365))}y`
}

export function dateTime(iso: string): string {
  return format(new Date(iso), "MMM d, yyyy HH:mm:ss")
}

const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 })
export function count(n: number): string {
  return n < 1000 ? String(n) : compact.format(n)
}

export function percent(part: number, total: number): string {
  if (!total) return "0%"
  const p = (part / total) * 100
  return p >= 1 || p === 0 ? `${Math.round(p)}%` : "<1%"
}

/** Splits "Type: value" titles so the type can be emphasised. */
export function splitTitle(title: string): { type: string; value: string } {
  const i = title.indexOf(": ")
  if (i <= 0 || i > 80) return { type: title, value: "" }
  return { type: title.slice(0, i), value: title.slice(i + 2) }
}

const adapterNames: Record<string, string> = { sentry: "Sentry", appsignal: "AppSignal", "appsignal-frontend": "AppSignal (browser)" }
/** Display name for an ingestion adapter, e.g. "appsignal" -> "AppSignal". */
export function adapterLabel(name: string): string {
  return adapterNames[name] ?? name.charAt(0).toUpperCase() + name.slice(1)
}

const commitSHA = /^[0-9a-f]{12,40}$/
/** Abbreviates a release that is a full commit SHA, as git does: "b240901". */
export function shortRelease(release: string): string {
  return commitSHA.test(release) ? release.slice(0, 7) : release
}

/** Links a commit-SHA release to the project's repository, when one is set. */
export function commitURL(repoURL: string | undefined, release: string): string | undefined {
  if (!repoURL || !commitSHA.test(release)) return undefined
  return `${repoURL}/commit/${release}`
}

/** One or two initials for an avatar: "Ada Lovelace" -> "AL", "ada@x.io" -> "A". */
export function initials(name: string): string {
  const words = name.split("@")[0].split(/[\s._-]+/).filter(Boolean)
  return (words.length > 1 ? words[0][0] + words[words.length - 1][0] : (words[0]?.[0] ?? "?")).toUpperCase()
}
