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

// Re-obtain the synchronizer token after a reload dropped module memory.
// POSTs /api/session/refresh with cookies; on success the server rotates
// the session and returns a fresh csrf_token. Failures leave the token
// empty so callers fall through to the existing 403/401 error UI.
async function refreshCsrf(): Promise<void> {
  try {
    const res = await fetch("/api/session/refresh", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
    })
    if (!res.ok) return
    const body = (await res.json()) as { csrf_token?: unknown }
    if (typeof body.csrf_token === "string" && body.csrf_token !== "") {
      csrfToken = body.csrf_token
    }
  } catch {
    // Leave the token empty; the mutating call below 403s into existing UI.
  }
}

// Restore the CSRF token on boot when module memory is empty but the
// session/refresh cookies survived a reload. No-op when already set.
export async function ensureCsrf(): Promise<void> {
  if (csrfToken) return
  await refreshCsrf()
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase()
  const isMutatingApi =
    (method === "POST" ||
      method === "PUT" ||
      method === "PATCH" ||
      method === "DELETE") &&
    path.startsWith("/api/") &&
    path !== "/api/session" &&
    path !== "/api/session/refresh"
  const doFetch = (): Promise<Response> => {
    const extra = (init?.headers ?? {}) as Record<string, string>
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...extra,
    }
    if (isMutatingApi && csrfToken) {
      headers["X-Yodea-CSRF"] = csrfToken
    }
    return fetch(path, {
      credentials: "same-origin",
      ...init,
      headers,
    })
  }
  let res = await doFetch()
  // After a reload the module token is empty while the session cookie
  // survives, so the first write 403s with {"error":"csrf required"}.
  // Refresh once and retry once, then surface the error.
  if (res.status === 403 && isMutatingApi) {
    let isCsrf = false
    try {
      const text = await res.clone().text()
      isCsrf = text.toLowerCase().includes("csrf")
    } catch {
      isCsrf = false
    }
    if (isCsrf) {
      await refreshCsrf()
      res = await doFetch()
    }
  }
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
  // Mirror the server: browsers normalize \ to /, so reject backslashes,
  // control chars, and encoded separators/nulls before allowing anything.
  if (raw.includes("\\")) return "/"
  if (/[\x00-\x1f\x7f]/.test(raw)) return "/"
  const loweredRaw = raw.toLowerCase()
  if (
    loweredRaw.includes("%5c") ||
    loweredRaw.includes("%2f") ||
    loweredRaw.includes("%00")
  )
    return "/"
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
