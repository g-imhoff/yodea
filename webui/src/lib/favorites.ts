import { useCallback, useEffect, useRef, useState } from "react"

import {
  addFavorite,
  AuthError,
  getFavorites,
  removeFavorite,
  type Favorite,
} from "@/lib/api"
import type { LoadStatus } from "@/components/dashboard"

/** Result of a star toggle. AuthError is still routed to onAuthError first;
 *  callers must stay silent for it (login redirect) and show retry UI only
 *  for non-auth failures. */
export type ToggleFavoriteResult = { ok: true } | { ok: false; error: unknown }

/** Server-backed favorites (GET/POST/DELETE /api/favorites, frozen
 *  contracts owned by the server node). Favorites are private per viewer;
 *  the server drops deleted previews from the list, so orphans disappear
 *  on the next load. Failures revert to the last loaded state; an expired
 *  session hands control back to the login phase via onAuthError. */
export function useFavorites(opts: { onAuthError: () => void }) {
  const [favorites, setFavorites] = useState<Favorite[]>([])
  const [status, setStatus] = useState<LoadStatus>("loading")
  const authErrorRef = useRef(opts.onAuthError)
  useEffect(() => {
    authErrorRef.current = opts.onAuthError
  }, [opts.onAuthError])

  const loadFavorites = useCallback(async () => {
    setStatus("loading")
    try {
      const res = await getFavorites()
      setFavorites(res.favorites ?? [])
      setStatus("ready")
    } catch (err) {
      if (err instanceof AuthError) {
        authErrorRef.current()
        return
      }
      setStatus("error")
    }
  }, [])

  const clearFavorites = useCallback(() => {
    setFavorites([])
    setStatus("loading")
  }, [])

  const toggleFavorite = useCallback(
    async (label: string): Promise<ToggleFavoriteResult> => {
      const saved = favorites.some((f) => f.label === label)
      if (saved) {
        const previous = favorites
        setFavorites(previous.filter((f) => f.label !== label))
        try {
          await removeFavorite(label)
        } catch (err) {
          if (err instanceof AuthError) {
            authErrorRef.current()
            return { ok: false, error: err }
          }
          setFavorites(previous)
          return { ok: false, error: err }
        }
        return { ok: true }
      }
      try {
        const row = await addFavorite(label)
        setFavorites((prev) =>
          prev.some((f) => f.label === row.label) ? prev : [...prev, row]
        )
      } catch (err) {
        if (err instanceof AuthError) {
          authErrorRef.current()
          return { ok: false, error: err }
        }
        // Leave the star unfilled; the caller surfaces the error with retry UI.
        return { ok: false, error: err }
      }
      return { ok: true }
    },
    [favorites]
  )

  return { favorites, favStatus: status, loadFavorites, clearFavorites, toggleFavorite }
}
