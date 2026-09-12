export interface Site {
  user_id: string
  project: string
  label: string
  updated_at: string
  files: number
  bytes: number
}

export interface View {
  label: string
  at: string
}

export interface Favorite {
  label: string
  link: string
  owner: string
  project: string
}

export class AuthError extends Error {
  constructor(message = "login required") {
    super(message)
    this.name = "AuthError"
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    ...init,
  })
  if (res.status === 401) throw new AuthError()
  if (!res.ok) throw new Error(`request failed: ${res.status}`)
  return (await res.json()) as T
}

export function login(
  email: string,
  password: string
): Promise<{ user_id: string }> {
  return request("/api/session", {
    method: "POST",
    body: JSON.stringify({ email, password }),
  })
}

export function logout(): Promise<{ status: string }> {
  return request("/api/logout", { method: "POST" })
}

export function getSites(): Promise<{ sites: Site[] }> {
  return request("/api/sites")
}

export function getViews(): Promise<{ views: View[] }> {
  return request("/api/views")
}

export function getFavorites(): Promise<{ favorites: Favorite[] }> {
  return request("/api/favorites")
}

export function addFavorite(label: string): Promise<Favorite> {
  return request("/api/favorites", {
    method: "POST",
    body: JSON.stringify({ label }),
  })
}

export function removeFavorite(label: string): Promise<{ status: string }> {
  return request(`/api/favorites/${encodeURIComponent(label)}`, {
    method: "DELETE",
  })
}

/**
 * Mirror of the server's safe next-URL allowlist (safeNext in
 * internal/server/server.go): a relative central path with a single leading
 * slash, or an https URL on the central host itself. Preview-subdomain and
 * any other absolute targets fall back to "/": the server strips those
 * ?next= values to bare /login, so honoring them here would disagree with
 * the post-login landing page.
 */
export function safeNext(raw: string | null, host: string): string {
  if (!raw) return "/"
  const central = host.split(":")[0].toLowerCase()
  if (raw[0] === "/") {
    if (raw[1] === "/" || raw[1] === "\\") return "/"
    const lower = raw.toLowerCase()
    if (
      raw.includes("\\") ||
      raw.includes("\n") ||
      raw.includes("\r") ||
      raw.includes("\t") ||
      lower.includes("%5c") ||
      lower.includes("%2f") ||
      lower.includes("%00")
    )
      return "/"
    return raw
  }
  if (!raw.startsWith("https://")) return "/"
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return "/"
  }
  if (url.protocol !== "https:") return "/"
  // Reject userinfo and port tricks before comparing the host.
  if (url.username !== "" || url.password !== "" || url.port !== "") return "/"
  if (url.hostname.toLowerCase() !== central) return "/"
  const rawLower = raw.toLowerCase()
  if (
    raw.includes("\\") ||
    raw.includes("\n") ||
    raw.includes("\r") ||
    raw.includes("\t") ||
    rawLower.includes("%5c") ||
    rawLower.includes("%2f") ||
    rawLower.includes("%00")
  )
    return "/"
  return raw
}

/** Public URL of a preview label on the current central host. */
export function previewUrl(label: string): string {
  return `${window.location.protocol}//${label}.${window.location.host}/`
}

export function formatDateTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date)
}
