import { useCallback, useEffect, useState } from "react"

import { Dashboard, type LoadStatus } from "@/components/dashboard"
import { LoginPage } from "@/components/login-page"
import { Spinner } from "@/components/ui/spinner"
import {
  AuthError,
  ensureCsrf,
  getSites,
  getViews,
  logout,
  safeNext,
  type Site,
  type View,
} from "@/lib/api"
import { useFavorites } from "@/lib/favorites"

type Phase = "checking" | "login" | "app"

export function App() {
  const [phase, setPhase] = useState<Phase>("checking")
  const [sites, setSites] = useState<Site[]>([])
  const [views, setViews] = useState<View[]>([])
  const [sitesStatus, setSitesStatus] = useState<LoadStatus>("loading")
  const [viewsStatus, setViewsStatus] = useState<LoadStatus>("loading")
  const [loggingOut, setLoggingOut] = useState(false)
  const handleAuthError = useCallback(() => setPhase("login"), [])
  const {
    favorites,
    favStatus,
    loadFavorites,
    clearFavorites,
    toggleFavorite,
  } = useFavorites({ onAuthError: handleAuthError })
  const [next] = useState(() =>
    safeNext(
      new URLSearchParams(window.location.search).get("next"),
      window.location.host
    )
  )

  const loadSites = useCallback(async () => {
    setSitesStatus("loading")
    try {
      const res = await getSites()
      setSites(res.sites ?? [])
      setSitesStatus("ready")
    } catch (err) {
      if (err instanceof AuthError) {
        setPhase("login")
        return
      }
      setSitesStatus("error")
    }
  }, [])

  const loadViews = useCallback(async () => {
    setViewsStatus("loading")
    try {
      const res = await getViews()
      setViews(res.views ?? [])
      setViewsStatus("ready")
    } catch (err) {
      if (err instanceof AuthError) {
        setPhase("login")
        return
      }
      setViewsStatus("error")
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      // Restore the CSRF token dropped from module memory by a reload
      // before any star toggle can 403; the request path retries once
      // on csrf 403s as a backup. Refresh failure leaves the token empty
      // and the loaders below route to login/error UI as before.
      await ensureCsrf()
      if (cancelled) return
      await Promise.all([loadSites(), loadViews(), loadFavorites()])
      if (cancelled) return
      // Loaders route auth failures to the login phase themselves; only
      // advance a still-checking page so a login redirect is never undone.
      setPhase((p) => (p === "checking" ? "app" : p))
    })()
    return () => {
      cancelled = true
    }
  }, [loadSites, loadViews, loadFavorites])

  const handleLoggedIn = useCallback(
    (target: string) => {
      if (target !== "/") {
        window.location.href = target
        return
      }
      setPhase("checking")
      setSitesStatus("loading")
      setViewsStatus("loading")
      void (async () => {
        await Promise.all([loadSites(), loadViews(), loadFavorites()])
        setPhase((p) => (p === "checking" ? "app" : p))
      })()
    },
    [loadSites, loadViews, loadFavorites]
  )

  const handleLogout = useCallback(async () => {
    if (loggingOut) return
    setLoggingOut(true)
    try {
      await logout()
    } catch {
      // Logging out locally regardless; the session cookie is cleared server-side at best effort.
    } finally {
      setSites([])
      setViews([])
      clearFavorites()
      setLoggingOut(false)
      setPhase("login")
    }
  }, [loggingOut, clearFavorites])

  if (phase === "checking") {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <Spinner />
      </div>
    )
  }

  if (phase === "login") {
    return <LoginPage next={next} onLoggedIn={handleLoggedIn} />
  }

  return (
    <Dashboard
      sites={sites}
      sitesStatus={sitesStatus}
      views={views}
      viewsStatus={viewsStatus}
      favorites={favorites}
      favStatus={favStatus}
      onToggleFavorite={toggleFavorite}
      onRetrySites={loadSites}
      onRetryViews={loadViews}
      onRetryFavorites={loadFavorites}
      onLogout={handleLogout}
      loggingOut={loggingOut}
    />
  )
}

export default App
