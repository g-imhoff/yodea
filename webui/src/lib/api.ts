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

// Synchronizer token for cookie-authed writes: hex(sha256(access_token))[:32]
// issued as csrf_token by POST /api/session (and /api/session/refresh).
// Kept in module memory only so preview JS cannot steal it; cleared on logout.
let csrfToken: string | null = null

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase()
  const extra = (init?.headers ?? {}) as Record<string, string>
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...extra,
  }
  if (
    (method === "POST" ||
      method === "PUT" ||
      method === "PATCH" ||
      method === "DELETE") &&
    path.startsWith("/api/") &&
    csrfToken
  ) {
    headers["X-Yodea-CSRF"] = csrfToken
  }
  const res = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers,
  })
  if (res.status === 401) throw new AuthError()
  if (!res.ok) throw new Error(`request failed: ${res.status}`)
  return (await res.json()) as T
}

export async function login(
  email: string,
  password: string
): Promise<{ user_id: string; csrf_token: string }> {
  const res = await request<{ user_id: string; csrf_token: string }>(
    "/api/session",
    {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }
  )
  csrfToken = res.csrf_token ?? null
  return res
}

export async function logout(): Promise<{ status: string }> {
  try {
    return await request("/api/logout", { method: "POST" })
  } finally {
    csrfToken = null
  }
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
  if (raw.startsWith("/") && !raw.startsWith("//")) return raw
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
